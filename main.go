/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/metal-stack/v"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	databasev1 "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/controllers"
	"github.com/fi-ts/postgreslet/pkg/crdinstaller"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
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
	var metricsAddrCtrlMgr, metricsAddrSvcMgr, partitionID, tenant, ctrlClusterKubeconfig, pspName, lbIP string
	var enableLeaderElection bool
	var portRangeStart, portRangeSize int
	flag.StringVar(&metricsAddrSvcMgr, "metrics-addr-svc-mgr", ":8080", "The address the metric endpoint of the service cluster manager binds to.")
	flag.StringVar(&metricsAddrCtrlMgr, "metrics-addr-ctrl-mgr", "0", "The address the metric endpoint of the control cluster manager binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&partitionID, "partition-id", "", "The partition ID of the worker-cluster.")
	flag.StringVar(&tenant, "tenant", "", "The tenant name.")
	flag.StringVar(&ctrlClusterKubeconfig, "controlplane-kubeconfig", "/var/run/secrets/postgreslet/kube/config", "The path to the kubeconfig to talk to the control plane")
	flag.StringVar(&lbIP, "load-balancer-ip", "", "The load-balancer IP of postgres in this cluster. If not set one will be provisioned dynamically.")
	// todo: Check the default port range start and size.
	flag.IntVar(&portRangeStart, "port-range-start", 32000, "The start of the port range of services LoadBalancer.")
	flag.IntVar(&portRangeSize, "port-range-size", 8000, "The size of the port range of services LoadBalancer.")
	flag.StringVar(&pspName, "custom-psp-name", "postgres-operator-psp", "The namem of our custom PodSecurityPolicy. Will be added to the ClusterRoles.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	if len(lbIP) > 0 {
		// todo: Shift the logic to a dedicated pkg for args validation.
		if ip := net.ParseIP(lbIP); ip == nil {
			ctrl.Log.Error(nil, fmt.Sprintf("IP %s not valid", lbIP))
			os.Exit(1)
		}
	}

	// todo: Remove
	ctrl.Log.Info("flag",
		"metrics-addr-svc-mgr", metricsAddrSvcMgr,
		"metrics-addr-ctrl-mgr", metricsAddrCtrlMgr,
		"enable-leader-election", enableLeaderElection,
		"partition-id", partitionID,
		"tenant", tenant,
		"load-balancer-ip", lbIP,
		"port-range-start", portRangeStart,
		"port-range-size", portRangeSize)

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

	opMgr, err := operatormanager.New(svcClusterConf, "external/svc-postgres-operator.yaml", scheme, ctrl.Log.WithName("OperatorManager"), pspName)
	if err != nil {
		setupLog.Error(err, "unable to create `OperatorManager`")
		os.Exit(1)
	}

	if err = (&controllers.PostgresReconciler{
		CtrlClient:      ctrlPlaneClusterMgr.GetClient(),
		SvcClient:       svcClusterMgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("Postgres"),
		Scheme:          ctrlPlaneClusterMgr.GetScheme(),
		PartitionID:     partitionID,
		Tenant:          tenant,
		OperatorManager: opMgr,
		LBManager:       lbmanager.New(svcClusterMgr.GetClient(), lbIP, int32(portRangeStart), int32(portRangeSize)),
	}).SetupWithManager(ctrlPlaneClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Postgres")
		os.Exit(1)
	}

	if err = (&controllers.StatusReconciler{
		SvcClient:  svcClusterMgr.GetClient(),
		CtrlClient: ctrlPlaneClusterMgr.GetClient(),
		Log:        ctrl.Log.WithName("controllers").WithName("Status"),
		Scheme:     svcClusterMgr.GetScheme(),
	}).SetupWithManager(svcClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Status")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	ctx := context.Background()

	// update all existing operators to the current version
	if err := opMgr.UpdateAllOperators(ctx); err != nil {
		setupLog.Error(err, "error updating the postgres operators")
	}

	setupLog.Info("starting service cluster manager", "version", v.V)
	go func() {
		if err := svcClusterMgr.Start(ctx); err != nil {
			setupLog.Error(err, "problem running service cluster manager")
			os.Exit(1)
		}
	}()

	setupLog.Info("starting control plane cluster manager", "version", v.V)
	if err := ctrlPlaneClusterMgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running control plane cluster manager")
		os.Exit(1)
	}
}
