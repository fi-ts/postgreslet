/*


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

package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"time"

	"github.com/metal-stack/v"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	databasev1 "github.com/fi-ts/postgres-controller/api/v1"
	"github.com/fi-ts/postgres-controller/controllers"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = databasev1.AddToScheme(scheme)
	_ = zalando.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	conf := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(conf, ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "908dd13e.fits.cloud",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Use no-cache client to avoid waiting.
	newClient, err := client.New(conf, client.Options{})
	if err != nil {
		setupLog.Error(err, "unable to create a new client")
		os.Exit(1)
	}
	objs, err := installExternalYAML("./external.yaml", "example-partition", newClient)
	if err != nil {
		setupLog.Error(err, "unable to install external YAML")
		os.Exit(1)
	}
	defer uninstallExternalYaml(objs, newClient)

	if err = (&controllers.PostgresReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("Postgres"),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Postgres")
		os.Exit(1)
	}

	// if err = (&databasev1.Postgres{}).SetupWebhookWithManager(mgr); err != nil {
	// 	setupLog.Error(err, "unable to create webhook", "webhook", "Postgres")
	// 	os.Exit(1)
	// }

	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager", "version", v.V)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// todo: Refactor
func installExternalYAML(fileName, partitionID string, k8sClient client.Client) (objs []runtime.Object, err error) {
	bb, err := ioutil.ReadFile(fileName)
	if err != nil {
		return
	}

	ctx := context.Background()

	// Make sure the namespace `partitionID` exists.
	ns := &corev1.Namespace{}
	if err = k8sClient.Get(ctx, client.ObjectKey{Name: partitionID}, &corev1.Namespace{}); err != nil {
		ns.Name = partitionID
		if err = k8sClient.Create(ctx, ns); err != nil {
			return
		}
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	list := &corev1.List{}
	if _, _, err = deserializer.Decode(bb, nil, list); err != nil {
		return
	}

	// Decode each YAML to `runtime.Object`, add the namespace to it and install it.
	accessor := meta.NewAccessor()
	for _, item := range list.Items {
		obj, _, er := deserializer.Decode(item.Raw, nil, nil)
		if er != nil {
			return objs, er
		}
		if err = accessor.SetNamespace(obj, partitionID); err != nil {
			return
		}

		// Remove annotations.
		if err = accessor.SetAnnotations(obj, nil); err != nil {
			return
		}

		// Add the namespace to the `ServiceAccount` in the `ClusterRoleBinding`
		if v, ok := obj.(*v1.ClusterRoleBinding); ok {
			for i, s := range v.Subjects {
				if s.Kind == "ServiceAccount" {
					v.Subjects[i].Namespace = partitionID
				}
			}
			log.Println("clusterrolebinding", v)
		}

		if err = k8sClient.Create(ctx, obj); err != nil {
			return
		}
		objs = append(objs, obj)
	}

	// Wait till there's at least one `postgres-operator` pod.
	pods := &corev1.PodList{}
	if err = wait.Poll(time.Second, 30*time.Second, func() (bool, error) {
		if err := k8sClient.List(ctx, pods, client.MatchingLabels{"name": "postgres-operator"}); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if len(pods.Items) == 0 {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return
	}

	// Wait till at least one such pod is running.
	if err = wait.Poll(time.Second, 60*time.Second, func() (bool, error) {
		for _, pod := range pods.Items {
			newPod := &corev1.Pod{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, newPod); err != nil {
				return false, err
			}
			if newPod.Status.Phase == corev1.PodRunning {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return
	}

	return
}

func uninstallExternalYaml(objs []runtime.Object, k8sClient client.Client) error {
	for _, obj := range objs {
		if err := k8sClient.Delete(context.Background(), obj); err != nil {
			return err
		}
	}
	return nil
}
