/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/metal-stack/v"
	coreosv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	databasev1 "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/controllers"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
	firewall "github.com/metal-stack/firewall-controller/api/v1"
	"github.com/spf13/viper"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	// +kubebuilder:scaffold:imports
)

const (
	// envPrefix               = "pg"
	metricsAddrSvcMgrFlg    = "metrics-addr-svc-mgr"
	metricsAddrCtrlMgrFlg   = "metrics-addr-ctrl-mgr"
	enableLeaderElectionFlg = "enable-leader-election"
	partitionIDFlg          = "partition-id"
	tenantFlg               = "tenant"
	ctrlPlaneKubeConfifgFlg = "controlplane-kubeconfig"
	loadBalancerIPFlg       = "load-balancer-ip"
	portRangeStartFlg       = "port-range-start"
	portRangeSizeFlg        = "port-range-size"
	customPSPNameFlg        = "custom-psp-name"
	storageClassFlg         = "storage-class"
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
	_ = coreosv1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddrCtrlMgr, metricsAddrSvcMgr, partitionID, tenant, ctrlClusterKubeconfig, pspName, lbIP, storageClass string
	var enableLeaderElection bool
	var portRangeStart, portRangeSize int

	// viper.SetEnvPrefix(envPrefix)
	viper.AutomaticEnv()
	replacer := strings.NewReplacer("-", "_")
	viper.SetEnvKeyReplacer(replacer)

	viper.SetDefault(metricsAddrSvcMgrFlg, ":8080")
	metricsAddrSvcMgr = viper.GetString(metricsAddrSvcMgrFlg)

	viper.SetDefault(metricsAddrCtrlMgrFlg, "0")
	metricsAddrCtrlMgr = viper.GetString(metricsAddrCtrlMgrFlg)

	viper.SetDefault(enableLeaderElectionFlg, false)
	enableLeaderElection = viper.GetBool(enableLeaderElectionFlg)

	partitionID = viper.GetString(partitionIDFlg)

	tenant = viper.GetString(tenantFlg)

	viper.SetDefault(ctrlPlaneKubeConfifgFlg, "/var/run/secrets/postgreslet/kube/config")
	ctrlClusterKubeconfig = viper.GetString(ctrlPlaneKubeConfifgFlg)

	lbIP = viper.GetString(loadBalancerIPFlg)
	if len(lbIP) > 0 {
		// todo: Shift the logic to a dedicated pkg for args validation.
		if ip := net.ParseIP(lbIP); ip == nil {
			ctrl.Log.Error(nil, fmt.Sprintf("Cannot parse provided %s %q, exiting.", loadBalancerIPFlg, lbIP))
			os.Exit(1)
		}
	}

	// todo: Check the default port range start and size.
	viper.SetDefault(portRangeStartFlg, 32000)
	portRangeStart = viper.GetInt(portRangeStartFlg)
	viper.SetDefault(portRangeSizeFlg, 8000)
	portRangeSize = viper.GetInt(portRangeSizeFlg)

	viper.SetDefault(customPSPNameFlg, "postgres-operator-psp")
	pspName = viper.GetString(customPSPNameFlg)

	storageClass = viper.GetString(storageClassFlg)

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	ctrl.Log.Info("flag",
		metricsAddrSvcMgrFlg, metricsAddrSvcMgr,
		metricsAddrCtrlMgrFlg, metricsAddrCtrlMgr,
		enableLeaderElectionFlg, enableLeaderElection,
		partitionIDFlg, partitionID,
		tenantFlg, tenant,
		ctrlPlaneKubeConfifgFlg, ctrlClusterKubeconfig,
		loadBalancerIPFlg, lbIP,
		portRangeStartFlg, portRangeStart,
		portRangeSizeFlg, portRangeSize,
		customPSPNameFlg, pspName,
		storageClassFlg, storageClass,
	)

	svcClusterConf := ctrl.GetConfigOrDie()
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

	ctrlPlaneClusterConf, err := clientcmd.BuildConfigFromFlags("", ctrlClusterKubeconfig)
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
		StorageClass:    storageClass,
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
