/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package crdinstaller

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CRDInstaller installs CRD
type CRDInstaller struct {
	client.Client
	*runtime.Scheme
	Log logr.Logger
}

// New constructs a new CRDInstaller
func New(conf *rest.Config, scheme *runtime.Scheme, log logr.Logger) (*CRDInstaller, error) {
	// Use no-cache client to avoid waiting for cashing.
	client, err := client.New(conf, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("error while creating new k8s client: %v", err)
	}
	log.Info("new `CRDInstaller` created")
	return &CRDInstaller{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}, nil
}

// Install insalls the CRD given in filePath
func (i *CRDInstaller) Install(filePath string) error {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("error while reading CRD file: %v", err)
	}

	obj, _, err := serializer.NewCodecFactory(i.Scheme).UniversalDeserializer().Decode(bytes, nil, nil)
	if err != nil {
		return fmt.Errorf("error while deserializing bytes: %v", err)
	}

	crdRead, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return errors.New("no CRD in the file")
	}

	ctx := context.Background()
	name := crdRead.Name

	// Remove redundant info.
	crdRead.ObjectMeta = metav1.ObjectMeta{Name: name}
	crdRead.Status = apiextensionsv1.CustomResourceDefinitionStatus{}

	// Fetch the CRD and create one if not found.
	crdFetched := &apiextensionsv1.CustomResourceDefinition{}
	if err := i.Get(ctx, client.ObjectKey{Name: name}, crdFetched); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error while fetching CRD Postgresql: %v", err)
		}

		if err := i.Create(ctx, crdRead); err != nil {
			return fmt.Errorf("error while creating CRD Postgresql: %v", err)
		}
		i.Log.Info("CRD newly installed")
		return nil
	}

	i.Log.Info("CRD installed")
	return nil
}
