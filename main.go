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
	"github.com/fi-ts/postgreslet/pkg/etcdmanager"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
	firewall "github.com/metal-stack/firewall-controller/api/v1"
	"github.com/spf13/viper"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	// +kubebuilder:scaffold:imports
)

const (
	// envPrefix               = "pg"
	metricsAddrSvcMgrFlg                  = "metrics-addr-svc-mgr"
	metricsAddrCtrlMgrFlg                 = "metrics-addr-ctrl-mgr"
	enableLeaderElectionFlg               = "enable-leader-election"
	partitionIDFlg                        = "partition-id"
	tenantFlg                             = "tenant"
	ctrlPlaneKubeConfifgFlg               = "controlplane-kubeconfig"
	loadBalancerIPFlg                     = "load-balancer-ip"
	portRangeStartFlg                     = "port-range-start"
	portRangeSizeFlg                      = "port-range-size"
	customPSPNameFlg                      = "custom-psp-name"
	storageClassFlg                       = "storage-class"
	postgresImageFlg                      = "postgres-image"
	etcdHostFlg                           = "etcd-host"
	crdValidationFlg                      = "enable-crd-validation"
	operatorImageFlg                      = "operator-image"
	pgParamBlockListFlg                   = "postgres-param-blocklist"
	majorVersionUpgradeModeFlg            = "major-version-upgrade-mode"
	standbyClustersSourceRangesFlg        = "standby-clusters-source-ranges"
	postgresletNamespaceFlg               = "postgreslet-namespace"
	sidecarsCMNameFlg                     = "sidecars-configmap-name"
	enableNetPolFlg                       = "enable-netpol"
	enablePodAntiaffinityFlg              = "enable-pod-antiaffinity"
	patroniRetryTimeoutFlg                = "patroni-retry-timeout"
	enableStandbyLeaderSelectorFlg        = "enable-standby-leader-selector"
	ControlPlaneNamespaceFlg              = "control-plane-namespace"
	enableLegacyStandbySelectorFlg        = "enable-legacy-standby-selector"
	deployEtcdFlg                         = "deploy-etcd"
	etcdImageFlg                          = "etcd-image"
	etcdBackupSidecarImageFlg             = "etcd-backup-sidecar-image"
	etcdBackupSecretNameFlg               = "etcd-backup-secret-name" // nolint
	etcdPSPNameFlg                        = "etcd-psp-name"
	postgresletFullnameFlg                = "postgreslet-fullname"
	enableLBSourceRangesFlg               = "enable-lb-source-ranges"
	enableRandomStorageEncrytionSecretFlg = "enable-random-storage-encryption-secret"
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

	var (
		metricsAddrCtrlMgr      string
		metricsAddrSvcMgr       string
		partitionID             string
		tenant                  string
		ctrlClusterKubeconfig   string
		pspName                 string
		lbIP                    string
		storageClass            string
		postgresImage           string
		etcdHost                string
		operatorImage           string
		majorVersionUpgradeMode string
		postgresletNamespace    string
		sidecarsCMName          string
		controlPlaneNamespace   string
		etcdImage               string
		etcdBackupSidecarImage  string
		etcdBackupSecretName    string
		etcdPSPName             string
		postgresletFullname     string

		enableLeaderElection               bool
		enableCRDValidation                bool
		enableNetPol                       bool
		enablePodAntiaffinity              bool
		enableStandbyLeaderSelector        bool
		enableLegacyStandbySelector        bool
		deployEtcd                         bool
		enableLBSourceRanges               bool
		enableRandomStorageEncrytionSecret bool

		portRangeStart int
		portRangeSize  int

		patroniTTL          uint32
		patroniLoopWait     uint32
		patroniRetryTimeout uint32

		pgParamBlockList map[string]bool

		standbyClusterSourceRanges []string
	)

	// TODO enable Prefix and update helm chart
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

	// TODO move all the GetStrings to the controllers where they are needed and don't pass along those strings.
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

	operatorImage = viper.GetString(operatorImageFlg)
	postgresImage = viper.GetString(postgresImageFlg)

	etcdHost = viper.GetString(etcdHostFlg)

	viper.SetDefault(crdValidationFlg, true)
	enableCRDValidation = viper.GetBool(crdValidationFlg)

	// read the (space-separated) list of configured blocked params
	blockedPgParams := viper.GetStringSlice(pgParamBlockListFlg)
	// and copy them in a map for easier access
	pgParamBlockList = make(map[string]bool, len(blockedPgParams))
	for _, blockedParam := range blockedPgParams {
		pgParamBlockList[blockedParam] = true
	}

	viper.SetDefault(majorVersionUpgradeModeFlg, "manual")
	majorVersionUpgradeMode = viper.GetString(majorVersionUpgradeModeFlg)

	// read the (space-separated) list of configured blocked params
	viper.SetDefault(standbyClustersSourceRangesFlg, "255.255.255.255/32")
	standbyClusterSourceRanges = viper.GetStringSlice(standbyClustersSourceRangesFlg)

	viper.SetDefault(postgresletNamespaceFlg, "postgreslet-system")
	postgresletNamespace = viper.GetString(postgresletNamespaceFlg)

	viper.SetDefault(sidecarsCMNameFlg, "postgreslet-postgres-sidecars")
	sidecarsCMName = viper.GetString(sidecarsCMNameFlg)

	viper.SetDefault(enableNetPolFlg, false)
	enableNetPol = viper.GetBool(enableNetPolFlg)

	viper.SetDefault(enablePodAntiaffinityFlg, false)
	enablePodAntiaffinity = viper.GetBool(enablePodAntiaffinityFlg)

	// hard coded value
	patroniLoopWait = databasev1.DefaultPatroniParamValueLoopWait

	// user defined value
	viper.SetDefault(patroniRetryTimeoutFlg, databasev1.DefaultPatroniParamValueRetryTimeout)
	patroniRetryTimeout = viper.GetUint32(patroniRetryTimeoutFlg)

	// derived value
	patroniTTL = (2 * patroniRetryTimeout) + patroniLoopWait

	viper.SetDefault(enableStandbyLeaderSelectorFlg, true)
	enableStandbyLeaderSelector = viper.GetBool(enableStandbyLeaderSelectorFlg)

	viper.SetDefault(ControlPlaneNamespaceFlg, "metal-extension-postgres")
	controlPlaneNamespace = viper.GetString(ControlPlaneNamespaceFlg)

	viper.SetDefault(enableLegacyStandbySelectorFlg, false)
	enableLegacyStandbySelector = viper.GetBool(enableLegacyStandbySelectorFlg)

	viper.SetDefault(deployEtcdFlg, false)
	deployEtcd = viper.GetBool(deployEtcdFlg)

	etcdImage = viper.GetString(etcdImageFlg)
	etcdBackupSidecarImage = viper.GetString(etcdBackupSidecarImageFlg)
	viper.SetDefault(etcdBackupSecretNameFlg, "pgaas-etcd-s3-credentials")
	etcdBackupSecretName = viper.GetString(etcdBackupSecretNameFlg)

	viper.SetDefault(etcdPSPNameFlg, pspName)
	etcdPSPName = viper.GetString(etcdPSPNameFlg)

	viper.SetDefault(postgresletFullnameFlg, partitionID) // fall back to partition id
	postgresletFullname = viper.GetString(postgresletFullnameFlg)

	viper.SetDefault(enableLBSourceRangesFlg, true)
	enableLBSourceRanges = viper.GetBool(enableLBSourceRangesFlg)

	viper.SetDefault(enableRandomStorageEncrytionSecretFlg, false)
	enableRandomStorageEncrytionSecret = viper.GetBool(enableRandomStorageEncrytionSecretFlg)

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
		operatorImageFlg, operatorImage,
		postgresImageFlg, postgresImage,
		etcdHostFlg, etcdHost,
		crdValidationFlg, enableCRDValidation,
		pgParamBlockListFlg, pgParamBlockList,
		majorVersionUpgradeModeFlg, majorVersionUpgradeMode,
		standbyClustersSourceRangesFlg, standbyClusterSourceRanges,
		postgresletNamespaceFlg, postgresletNamespace,
		sidecarsCMNameFlg, sidecarsCMName,
		enableNetPolFlg, enableNetPol,
		enablePodAntiaffinityFlg, enablePodAntiaffinity,
		patroniRetryTimeoutFlg, patroniRetryTimeout,
		enableStandbyLeaderSelectorFlg, enableStandbyLeaderSelector,
		ControlPlaneNamespaceFlg, controlPlaneNamespace,
		enableLegacyStandbySelectorFlg, enableLegacyStandbySelector,
		deployEtcdFlg, deployEtcd,
		etcdImageFlg, etcdImage,
		etcdBackupSidecarImageFlg, etcdBackupSidecarImage,
		etcdBackupSecretNameFlg, etcdBackupSecretName,
		etcdPSPNameFlg, etcdPSPName,
		postgresletFullnameFlg, postgresletFullname,
		enableLBSourceRangesFlg, enableLBSourceRanges,
		enableRandomStorageEncrytionSecretFlg, enableRandomStorageEncrytionSecret,
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

	var etcdMgrOpts etcdmanager.Options = etcdmanager.Options{
		EtcdImage:              etcdImage,
		EtcdBackupSidecarImage: etcdBackupSidecarImage,
		SecretKeyRefName:       etcdBackupSecretName,
		PostgresletNamespace:   postgresletNamespace,
		PartitionID:            partitionID,
		PSPName:                etcdPSPName,
		PostgresletFullname:    postgresletFullname,
	}
	etcdMgr, err := etcdmanager.New(svcClusterConf, "external/svc-etcd.yaml", scheme, ctrl.Log.WithName("EtcdManager"), etcdMgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to create `EtcdManager`")
		os.Exit(1)
	}
	if deployEtcd {
		if err = etcdMgr.InstallOrUpdateEtcd(); err != nil {
			setupLog.Error(err, "unable to deploy etcd")
			os.Exit(1)
		}
	} else {
		if err = etcdMgr.UninstallEtcd(); err != nil {
			setupLog.Error(err, "unable to undeploy etcd")
		}
	}

	var opMgrOpts operatormanager.Options = operatormanager.Options{
		PspName:                 pspName,
		OperatorImage:           operatorImage,
		DockerImage:             postgresImage,
		EtcdHost:                etcdHost,
		CRDValidation:           enableCRDValidation,
		MajorVersionUpgradeMode: majorVersionUpgradeMode,
		PostgresletNamespace:    postgresletNamespace,
		SidecarsConfigMapName:   sidecarsCMName,
		PodAntiaffinity:         enablePodAntiaffinity,
		PartitionID:             partitionID,
	}
	opMgr, err := operatormanager.New(svcClusterConf, "external/svc-postgres-operator.yaml", scheme, ctrl.Log.WithName("OperatorManager"), opMgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to create `OperatorManager`")
		os.Exit(1)
	}

	var lbMgrOpts lbmanager.Options = lbmanager.Options{
		LBIP:                        lbIP,
		PortRangeStart:              int32(portRangeStart),
		PortRangeSize:               int32(portRangeSize),
		EnableStandbyLeaderSelector: enableStandbyLeaderSelector,
		EnableLegacyStandbySelector: enableLegacyStandbySelector,
		StandbyClustersSourceRanges: standbyClusterSourceRanges,
		EnableLBSourceRanges:        enableLBSourceRanges,
	}
	if err = (&controllers.PostgresReconciler{
		CtrlClient:                          ctrlPlaneClusterMgr.GetClient(),
		SvcClient:                           svcClusterMgr.GetClient(),
		Log:                                 ctrl.Log.WithName("controllers").WithName("Postgres"),
		Scheme:                              ctrlPlaneClusterMgr.GetScheme(),
		PartitionID:                         partitionID,
		Tenant:                              tenant,
		StorageClass:                        storageClass,
		OperatorManager:                     opMgr,
		LBManager:                           lbmanager.New(svcClusterMgr.GetClient(), lbMgrOpts),
		PgParamBlockList:                    pgParamBlockList,
		StandbyClustersSourceRanges:         standbyClusterSourceRanges,
		PostgresletNamespace:                postgresletNamespace,
		SidecarsConfigMapName:               sidecarsCMName,
		EnableNetPol:                        enableNetPol,
		EtcdHost:                            etcdHost,
		PatroniTTL:                          patroniTTL,
		PatroniLoopWait:                     patroniLoopWait,
		PatroniRetryTimeout:                 patroniRetryTimeout,
		EnableRandomStorageEncryptionSecret: enableRandomStorageEncrytionSecret,
	}).SetupWithManager(ctrlPlaneClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Postgres")
		os.Exit(1)
	}

	if err = (&controllers.StatusReconciler{
		SvcClient:             svcClusterMgr.GetClient(),
		CtrlClient:            ctrlPlaneClusterMgr.GetClient(),
		Log:                   ctrl.Log.WithName("controllers").WithName("Status"),
		Scheme:                svcClusterMgr.GetScheme(),
		PartitionID:           partitionID,
		ControlPlaneNamespace: controlPlaneNamespace,
	}).SetupWithManager(svcClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Status")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	ctx := context.Background()

	// update all existing operators to the current version
	if err := opMgr.UpdateAllManagedOperators(ctx); err != nil {
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
