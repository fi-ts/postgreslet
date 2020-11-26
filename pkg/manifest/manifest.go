/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// shameless copy from kubernetes-sigs/controller-runtime

package manifest

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Required to make statik work
	_ "github.com/fi-ts/postgres-controller/statik"
	"github.com/rakyll/statik/fs"

	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

var log = ctrl.Log.WithName("manifests")

// InstallOptions are the options for installing Manifests
type InstallOptions struct {
	// UseVFS if set to true, built-in content created by statik is used instead of real fs
	UseVFS bool
	// Paths is a list of paths to the directories or files containing Manifests
	Paths []string

	// Manifests is a list of Manifests to install
	Manifests []runtime.Object

	// ManifestContents is a map of Manifests filename to byte array, e.g. useful for embedded static content
	ManifestContents map[string][]byte

	// ErrorIfPathMissing will cause an error if a Path does not exist
	ErrorIfPathMissing bool

	// MaxTime is the max time to wait
	MaxTime time.Duration

	// PollInterval is the interval to check
	PollInterval time.Duration

	// CleanUpAfterUse will cause the Manifests listed for installation to be
	// uninstalled when terminating the test environment.
	// Defaults to false.
	CleanUpAfterUse bool
}

const defaultPollInterval = 100 * time.Millisecond
const defaultMaxWait = 10 * time.Second

// InstallManifests installs a collection of Manifests into a cluster by reading the Manifest yaml files from a directory
func InstallManifests(config *rest.Config, options InstallOptions) ([]runtime.Object, error) {
	defaultOptions(&options)

	// Read the Manifest yamls into options.Manifests
	if err := readManifestFiles(&options); err != nil {
		return nil, err
	}

	// Create the Manifests in the apiserver
	if err := CreateManifests(config, options.Manifests); err != nil {
		return options.Manifests, err
	}

	// Wait for the Manifests to appear as Resources in the apiserver
	if err := WaitForManifests(config, options.Manifests, options); err != nil {
		return options.Manifests, err
	}

	return options.Manifests, nil
}

// readManifestFiles reads the directories of Manifests in options.Paths and adds the Manifest structs to options.Manifests
func readManifestFiles(options *InstallOptions) error {
	if options.UseVFS && len(options.Paths) > 0 {
		manifestContents, err := readCRDsFromVFS(options)
		if err != nil {
			return err
		}

		options.ManifestContents = manifestContents
	}
	if len(options.Paths) > 0 || len(options.ManifestContents) > 0 {
		manifestList, err := renderManifests(options)
		if err != nil {
			return err
		}

		options.Manifests = append(options.Manifests, manifestList...)
	}
	return nil
}

// defaultManifestOptions sets the default values for Manifests
func defaultOptions(o *InstallOptions) {
	if o.MaxTime == 0 {
		o.MaxTime = defaultMaxWait
	}
	if o.PollInterval == 0 {
		o.PollInterval = defaultPollInterval
	}
}

// WaitForManifests waits for the Manifests to appear in discovery
func WaitForManifests(config *rest.Config, Manifests []runtime.Object, options InstallOptions) error {
	// Add each Manifest to a map of GroupVersion to Resource
	waitingFor := map[schema.GroupVersion]*sets.String{}
	for _, Manifest := range runtimeManifestListToUnstructured(Manifests) {
		gvs := []schema.GroupVersion{}
		ManifestGroup, _, err := unstructured.NestedString(Manifest.Object, "spec", "group")
		if err != nil {
			return err
		}
		ManifestPlural, _, err := unstructured.NestedString(Manifest.Object, "spec", "names", "plural")
		if err != nil {
			return err
		}
		ManifestVersion, _, err := unstructured.NestedString(Manifest.Object, "spec", "version")
		if err != nil {
			return err
		}
		if ManifestVersion != "" {
			gvs = append(gvs, schema.GroupVersion{Group: ManifestGroup, Version: ManifestVersion})
		}

		versions, _, err := unstructured.NestedSlice(Manifest.Object, "spec", "versions")
		if err != nil {
			return err
		}
		for _, version := range versions {
			versionMap, ok := version.(map[string]interface{})
			if !ok {
				continue
			}
			served, _, err := unstructured.NestedBool(versionMap, "served")
			if err != nil {
				return err
			}
			if served {
				versionName, _, err := unstructured.NestedString(versionMap, "name")
				if err != nil {
					return err
				}
				gvs = append(gvs, schema.GroupVersion{Group: ManifestGroup, Version: versionName})
			}
		}

		for _, gv := range gvs {
			log.V(1).Info("adding API in waitlist", "GV", gv)
			if _, found := waitingFor[gv]; !found {
				// Initialize the set
				waitingFor[gv] = &sets.String{}
			}
			// Add the Resource
			waitingFor[gv].Insert(ManifestPlural)
		}
	}

	// Poll until all resources are found in discovery
	p := &poller{config: config, waitingFor: waitingFor}
	return wait.PollImmediate(options.PollInterval, options.MaxTime, p.poll)
}

// poller checks if all the resources have been found in discovery, and returns false if not
type poller struct {
	// config is used to get discovery
	config *rest.Config

	// waitingFor is the map of resources keyed by group version that have not yet been found in discovery
	waitingFor map[schema.GroupVersion]*sets.String
}

// poll checks if all the resources have been found in discovery, and returns false if not
func (p *poller) poll() (done bool, err error) {
	// Create a new clientset to avoid any client caching of discovery
	cs, err := clientset.NewForConfig(p.config)
	if err != nil {
		return false, err
	}

	allFound := true
	for gv, resources := range p.waitingFor {
		// All resources found, do nothing
		if resources.Len() == 0 {
			delete(p.waitingFor, gv)
			continue
		}

		// Get the Resources for this GroupVersion
		// TODO: Maybe the controller-runtime client should be able to do this...
		resourceList, err := cs.Discovery().ServerResourcesForGroupVersion(gv.Group + "/" + gv.Version)
		if err != nil {
			return false, nil
		}

		// Remove each found resource from the resources set that we are waiting for
		for _, resource := range resourceList.APIResources {
			resources.Delete(resource.Name)
		}

		// Still waiting on some resources in this group version
		if resources.Len() != 0 {
			allFound = false
		}
	}
	return allFound, nil
}

// UninstallManifests uninstalls a collection of Manifests by reading the Manifest yaml files from a directory
func UninstallManifests(config *rest.Config, options InstallOptions) error {

	// Read the Manifest yamls into options.Manifests
	if err := readManifestFiles(&options); err != nil {
		return err
	}

	// Delete the Manifests from the apiserver
	cs, err := client.New(config, client.Options{})
	if err != nil {
		return err
	}

	// Uninstall each Manifest
	for _, Manifest := range runtimeManifestListToUnstructured(options.Manifests) {
		log.V(1).Info("uninstalling Manifest", "Manifest", Manifest.GetName())
		if err := cs.Delete(context.TODO(), Manifest); err != nil {
			// If Manifest is not found, we can consider success
			if !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// CreateManifests creates the Manifests
func CreateManifests(config *rest.Config, Manifests []runtime.Object) error {
	cs, err := client.New(config, client.Options{})
	if err != nil {
		return err
	}

	// Create each Manifest
	for _, Manifest := range runtimeManifestListToUnstructured(Manifests) {
		log.V(1).Info("installing Manifest", "Manifest", Manifest.GetName())
		existingManifest := Manifest.DeepCopy()
		err := cs.Get(context.TODO(), client.ObjectKey{Name: Manifest.GetName()}, existingManifest)
		switch {
		case apierrors.IsNotFound(err):
			if err := cs.Create(context.TODO(), Manifest); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			log.V(1).Info("Manifest already exists, updating", "Manifest", Manifest.GetName())
			Manifest.SetResourceVersion(existingManifest.GetResourceVersion())
			if err := cs.Update(context.TODO(), Manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

// renderManifests iterate through options.Paths and extract all Manifest files.
func renderManifests(options *InstallOptions) ([]runtime.Object, error) {
	var (
		err   error
		info  os.FileInfo
		files []os.FileInfo
	)

	type GVKN struct {
		GVK  schema.GroupVersionKind
		Name string
	}
	Manifests := map[GVKN]*unstructured.Unstructured{}

	for _, path := range options.Paths {
		var filePath = path

		// Return the error if ErrorIfPathMissing exists
		if info, err = os.Stat(path); os.IsNotExist(err) {
			if options.ErrorIfPathMissing {
				return nil, err
			}
			continue
		}

		if !info.IsDir() {
			filePath, files = filepath.Dir(path), []os.FileInfo{info}
		} else {
			if files, err = ioutil.ReadDir(path); err != nil {
				return nil, err
			}
		}

		log.V(1).Info("reading Manifests from path", "path", path)
		ManifestList, err := readManifests(filePath, files)
		if err != nil {
			return nil, err
		}

		for i, Manifest := range ManifestList {
			gvkn := GVKN{GVK: Manifest.GroupVersionKind(), Name: Manifest.GetName()}
			if _, found := Manifests[gvkn]; found {
				// Currently, we only print a log when there are duplicates. We may want to error out if that makes more sense.
				log.Info("there are more than one Manifest definitions with the same <Group, Version, Kind, Name>", "GVKN", gvkn)
			}
			// We always use the Manifest definition that we found last.
			Manifests[gvkn] = ManifestList[i]
		}
	}

	for name, bytes := range options.ManifestContents {
		log.V(1).Info("reading Manifests from bytes", "name", name)
		ManifestList, err := readManifest(name, bytes)
		if err != nil {
			return nil, err
		}

		for i, Manifest := range ManifestList {
			gvkn := GVKN{GVK: Manifest.GroupVersionKind(), Name: Manifest.GetName()}
			if _, found := Manifests[gvkn]; found {
				// Currently, we only print a log when there are duplicates. We may want to error out if that makes more sense.
				log.Info("there are more than one Manifest definitions with the same <Group, Version, Kind, Name>", "GVKN", gvkn)
			}
			// We always use the Manifest definition that we found last.
			Manifests[gvkn] = ManifestList[i]
		}
	}

	// Converting map to a list to return
	var res []runtime.Object
	for _, obj := range Manifests {
		res = append(res, obj)
	}
	return res, nil
}

// readManifests reads the Manifests from files and Unmarshals them into structs
func readManifest(name string, b []byte) ([]*unstructured.Unstructured, error) {
	// Unmarshal Manifests from file into structs
	docs, err := readDocumentsFromBytes(b)
	if err != nil {
		return nil, err
	}

	Manifests, err := readToUnstructured(docs)
	if err != nil {
		return nil, err
	}

	log.V(1).Info("read Manifests from file", "file", name)
	return Manifests, nil
}

// readManifests reads the Manifests from files and Unmarshals them into structs
func readManifests(basePath string, files []os.FileInfo) ([]*unstructured.Unstructured, error) {
	var Manifests []*unstructured.Unstructured

	// White list the file extensions that may contain Manifests
	ManifestExts := sets.NewString(".json", ".yaml", ".yml")

	for _, file := range files {
		// Only parse whitelisted file types
		if !ManifestExts.Has(filepath.Ext(file.Name())) {
			continue
		}

		// Unmarshal Manifests from file into structs
		docs, err := readDocuments(filepath.Join(basePath, file.Name()))
		if err != nil {
			return nil, err
		}

		cs, err := readToUnstructured(docs)
		if err != nil {
			return nil, err
		}
		Manifests = append(Manifests, cs...)

		log.V(1).Info("read Manifests from file", "file", file.Name())
	}
	return Manifests, nil
}

func readToUnstructured(docs [][]byte) ([]*unstructured.Unstructured, error) {
	var Manifests []*unstructured.Unstructured
	for _, doc := range docs {
		Manifest := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, Manifest); err != nil {
			return nil, err
		}

		// Check that it is actually a Manifest
		ManifestKind, _, err := unstructured.NestedString(Manifest.Object, "spec", "names", "kind")
		if err != nil {
			return nil, err
		}
		ManifestGroup, _, err := unstructured.NestedString(Manifest.Object, "spec", "group")
		if err != nil {
			return nil, err
		}

		if Manifest.GetKind() != "CustomResourceDefinition" || ManifestKind == "" || ManifestGroup == "" {
			continue
		}
		Manifests = append(Manifests, Manifest)
	}
	return Manifests, nil
}

// readDocuments reads documents from file
func readDocuments(fp string) ([][]byte, error) {
	b, err := ioutil.ReadFile(fp)
	if err != nil {
		return nil, err
	}
	return readDocumentsFromBytes(b)
}

// readDocuments reads documents from byte slice
func readDocumentsFromBytes(b []byte) ([][]byte, error) {
	docs := [][]byte{}
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
	for {
		// Read document
		doc, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func readCRDsFromVFS(options *InstallOptions) (map[string][]byte, error) {
	statikFS, err := fs.NewWithNamespace("config")
	if err != nil {
		log.Error(err, "unable to create virtual fs")
		return nil, err
	}
	crdMap := make(map[string][]byte)
	err = fs.Walk(statikFS, "/", func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		// Only consider crds
		usePath := false
		for _, p := range options.Paths {
			if strings.HasPrefix(path, p) {
				usePath = true
			}
		}
		if !usePath {
			return nil
		}
		b, readerr := fs.ReadFile(statikFS, path)
		if readerr != nil {
			return fmt.Errorf("unable to readfile:%v", readerr)
		}
		crdMap[path] = b
		log.Info("manifest", "path", path, "info", info)
		return nil
	})
	if err != nil {
		log.Error(err, "unable to read crs from virtual fs")
		return nil, err
	}
	return crdMap, nil
}
