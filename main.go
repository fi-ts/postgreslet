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
	"os/signal"
	"syscall"

	"github.com/metal-stack/v"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	databasev1 "github.com/fi-ts/postgres-controller/api/v1"
	"github.com/fi-ts/postgres-controller/controllers"
	"github.com/fi-ts/postgres-controller/pkg/crdinstaller"
	"github.com/fi-ts/postgres-controller/pkg/operatormanager"
	firewall "github.com/metal-stack/firewall-controller/api/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = clientgoscheme.AddToScheme(scheme)
	_ = databasev1.AddToScheme(scheme)
	_ = firewall.AddToScheme(scheme)
	_ = zalando.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddrCtrlMgr, metricsAddrSvcMgr, partitionID, tenant, ctrlClusterKubeconfig string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddrSvcMgr, "metrics-addr-svc-mgr", ":8080", "The address the metric endpoint of the service cluster manager binds to.")
	flag.StringVar(&metricsAddrCtrlMgr, "metrics-addr-ctrl-mgr", "0", "The address the metric endpoint of the control cluster manager binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&partitionID, "partition-id", "", "The partition ID of the worker-cluster.")
	flag.StringVar(&tenant, "tenant", "", "The tenant name.")
	flag.StringVar(&ctrlClusterKubeconfig, "controlplane-kubeconfig", "/var/run/secrets/postgreslet/kube/config", "The path to the kubeconfig to talk to the control plane")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// todo: Remove
	ctrl.Log.Info("flag",
		"metrics-addr-svc-mgr", metricsAddrSvcMgr,
		"metrics-addr-ctrl-mgr", metricsAddrCtrlMgr,
		"enable-leader-election", enableLeaderElection,
		"partition-id", partitionID,
		"tenant", tenant)

	svcClusterConf := ctrl.GetConfigOrDie()
	i, err := crdinstaller.New(svcClusterConf, scheme, ctrl.Log.WithName("CRDInstaller"))
	if err != nil {
		setupLog.Error(err, "unable to create `CRDInstaller`")
		os.Exit(1)
	}
	if err := i.Install("external/crd-postgresql.yaml"); err != nil {
		setupLog.Error(err, "unable to install CRD Postgresql")
		os.Exit(1)
	}

	svcClusterMgr, err := ctrl.NewManager(svcClusterConf, ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddrSvcMgr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "908dd13e.fits.cloud",
	})
	if err != nil {
		setupLog.Error(err, "unable to start service cluster manager")
		os.Exit(1)
	}

	ctrlPlaneClusterConf, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: ctrlClusterKubeconfig},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		setupLog.Error(err, "unable to get control cluster kubeconfig")
		os.Exit(1)
	}
	ctrlPlaneClusterMgr, err := ctrl.NewManager(ctrlPlaneClusterConf, ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddrCtrlMgr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "4d69ceab.fits.cloud",
	})
	if err != nil {
		setupLog.Error(err, "unable to start control plane cluster manager")
		os.Exit(1)
	}

	opMgr, err := operatormanager.New(svcClusterMgr.GetClient(), "external/svc-postgres-operator.yaml", scheme, ctrl.Log.WithName("OperatorManager"))
	if err != nil {
		setupLog.Error(err, "unable to create `OperatorManager`")
		os.Exit(1)
	}

	if err = (&controllers.PostgresReconciler{
		Client:          ctrlPlaneClusterMgr.GetClient(),
		Service:         svcClusterMgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("Postgres"),
		Scheme:          ctrlPlaneClusterMgr.GetScheme(),
		PartitionID:     partitionID,
		Tenant:          tenant,
		OperatorManager: opMgr,
	}).SetupWithManager(ctrlPlaneClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Postgres")
		os.Exit(1)
	}

	// if err = (&databasev1.Postgres{}).SetupWebhookWithManager(mgr); err != nil {
	// 	setupLog.Error(err, "unable to create webhook", "webhook", "Postgres")
	// 	os.Exit(1)
	// }

	if err = (&controllers.StatusReconciler{
		Client:  svcClusterMgr.GetClient(),
		Control: ctrlPlaneClusterMgr.GetClient(),
		Log:     ctrl.Log.WithName("controllers").WithName("Status"),
		Scheme:  svcClusterMgr.GetScheme(),
	}).SetupWithManager(svcClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Status")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting service cluster manager", "version", v.V)
	go func() {
		if err := svcClusterMgr.Start(setupSignalHandler()); err != nil {
			setupLog.Error(err, "problem running service cluster manager")
			os.Exit(1)
		}
	}()

	setupLog.Info("starting control plane cluster manager", "version", v.V)
	if err := ctrlPlaneClusterMgr.Start(setupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running control plane cluster manager")
		os.Exit(1)
	}
}

// setupSignalHandler is the same function as `signals.SetupSignalHandler` except it doesn't panic when it gets called twice.
func setupSignalHandler() <-chan struct{} {
	stop := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, []os.Signal{syscall.SIGINT, syscall.SIGTERM}...)
	go func() {
		<-c
		close(stop)

		// Exit upon the second SIGINT or SIGTERM.
		<-c
		os.Exit(1)
	}()
	return stop
}
