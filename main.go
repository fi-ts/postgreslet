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
	"flag"
	"os"

	"github.com/metal-stack/v"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	databasev1 "github.com/fi-ts/postgres-controller/api/v1"
	"github.com/fi-ts/postgres-controller/controllers"
	"github.com/fi-ts/postgres-controller/pkg/pgletconf"
	"github.com/fi-ts/postgres-controller/pkg/yamlmanager"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	pgletConf = &pgletconf.PostgresletConf{}
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
	flag.StringVar(&(pgletConf.PartitionID), "partition-id", "", "The partition ID of the worker-cluster.")
	flag.StringVar(&(pgletConf.Tenant), "tenant", "", "The tenant name.")
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

	y, err := yamlmanager.NewYAMLManager(conf, scheme)
	if err != nil {
		setupLog.Error(err, "unable to create a new external YAML manager")
		os.Exit(1)
	}
	objs, err := y.InstallYAML("./external.yaml", pgletConf.PartitionID)
	if err != nil {
		setupLog.Error(err, "unable to install external YAML")
		os.Exit(1)
	}
	defer func() {
		if err := y.UninstallYAML(objs); err != nil {
			setupLog.Error(err, "unable to uninstall external YAML")
		}
	}()

	if err = (&controllers.PostgresReconciler{
		Client:          mgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("Postgres"),
		Scheme:          mgr.GetScheme(),
		PostgresletConf: pgletConf,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Postgres")
		os.Exit(1)
	}

	// if err = (&databasev1.Postgres{}).SetupWebhookWithManager(mgr); err != nil {
	// 	setupLog.Error(err, "unable to create webhook", "webhook", "Postgres")
	// 	os.Exit(1)
	// }

	if err = (&controllers.StatusReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("Status"),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Status")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager", "version", v.V)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
