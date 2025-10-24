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
	"strings"
	"time"

	"github.com/metal-stack/v"
	coreosv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	databasev1 "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/controllers"
	"github.com/fi-ts/postgreslet/pkg/etcdmanager"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
	"github.com/fi-ts/postgreslet/pkg/webhooks"
	firewall "github.com/metal-stack/firewall-controller/v2/api/v1"
	"github.com/spf13/viper"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	// +kubebuilder:scaffold:imports
	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
)

const (
	// envPrefix               = "pg"

	metricsAddrSvcMgrFlg                        = "metrics-addr-svc-mgr"
	metricsAddrCtrlMgrFlg                       = "metrics-addr-ctrl-mgr"
	enableLeaderElectionFlg                     = "enable-leader-election"
	partitionIDFlg                              = "partition-id"
	tenantFlg                                   = "tenant"
	ctrlPlaneKubeConfifgFlg                     = "controlplane-kubeconfig"
	loadBalancerIPFlg                           = "load-balancer-ip"
	portRangeStartFlg                           = "port-range-start"
	portRangeSizeFlg                            = "port-range-size"
	customPSPNameFlg                            = "custom-psp-name"
	storageClassFlg                             = "storage-class"
	postgresImageFlg                            = "postgres-image"
	etcdHostFlg                                 = "etcd-host"
	crdRegistrationFlg                          = "enable-crd-registration"
	operatorImageFlg                            = "operator-image"
	pgParamBlockListFlg                         = "postgres-param-blocklist" // nolint
	majorVersionUpgradeModeFlg                  = "major-version-upgrade-mode"
	standbyClustersSourceRangesFlg              = "standby-clusters-source-ranges"
	postgresletNamespaceFlg                     = "postgreslet-namespace"
	sidecarsCMNameFlg                           = "sidecars-configmap-name"
	enableNetPolFlg                             = "enable-netpol"
	enablePodAntiaffinityFlg                    = "enable-pod-antiaffinity"
	patroniRetryTimeoutFlg                      = "patroni-retry-timeout"
	enableStandbyLeaderSelectorFlg              = "enable-standby-leader-selector"
	ControlPlaneNamespaceFlg                    = "control-plane-namespace"
	enableLegacyStandbySelectorFlg              = "enable-legacy-standby-selector"
	deployEtcdFlg                               = "deploy-etcd"
	etcdImageFlg                                = "etcd-image"
	etcdBackupSidecarImageFlg                   = "etcd-backup-sidecar-image"
	etcdBackupSecretNameFlg                     = "etcd-backup-secret-name" // nolint
	etcdPSPNameFlg                              = "etcd-psp-name"
	replicationChangeRequeueTimeFlg             = "replication-change-requeue-time-in-seconds"
	postgresletFullnameFlg                      = "postgreslet-fullname"
	enableLBSourceRangesFlg                     = "enable-lb-source-ranges"
	enableRandomStorageEncryptionSecretFlg      = "enable-random-storage-encryption-secret"
	enableWalGEncryptionFlg                     = "enable-walg-encryption"
	enableForceSharedIPFlg                      = "enable-force-shared-ip"
	initDBJobCMNameFlg                          = "initdb-job-configmap-name"
	enableBootstrapStandbyFromS3Flg             = "enable-bootsrtap-standby-from-s3"
	enableSuperUserForDBOFlg                    = "enable-superuser-for-dbo"
	tlsClusterIssuerFlg                         = "tls-cluster-issuer"
	tlsSubDomainFlg                             = "tls-sub-domain"
	enablePatroniFailsafeModeFlg                = "enable-patroni-failsafe-mode"
	enableFsGroupChangePolicyWebhookFlg         = "enable-fsgroup-change-policy-webhook"
	enableWalGExporterFlg                       = "enable-walg-exporter"
	walGExporterImageFlg                        = "walg-exporter-image"
	walGExporterCPULimitFlg                     = "walg-exporter-cpu-limit"
	walGExporterMemoryLimitFlg                  = "walg-exporter-memory-limit"
	podAntiaffinityPreferredDuringSchedulingFlg = "pod-antiaffinity-preferred-during-scheduling"
	podAntiaffinityTopologyKeyFlg               = "pod-antiaffinity-topology-key"
	enablePodTopologySpreadConstraintWebhookFlg = "enable-pod-topology-spread-constraint-webhook"
	podTopologySpreadConstraintTopologyKeyFlg   = "pod-topology-spread-constraint-topology-key"
	podTopologySpreadConstraintMaxSkewFlg       = "pod-topology-spread-constraint-max-skew"
	podTopologySpreadConstraintMinDomainsFlg    = "pod-topology-spread-constraint-min-domains"
	enableStandbyHostAttrsFlg                   = "enable-standby-host-attrs"
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
	_ = appsv1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
	_ = cmapi.AddToScheme(scheme)
}

func main() {

	var (
		metricsAddrCtrlMgr                     string
		metricsAddrSvcMgr                      string
		partitionID                            string
		tenant                                 string
		ctrlClusterKubeconfig                  string
		pspName                                string
		lbIP                                   string
		storageClass                           string
		postgresImage                          string
		etcdHost                               string
		operatorImage                          string
		majorVersionUpgradeMode                string
		postgresletNamespace                   string
		sidecarsCMName                         string
		controlPlaneNamespace                  string
		etcdImage                              string
		etcdBackupSidecarImage                 string
		etcdBackupSecretName                   string
		etcdPSPName                            string
		postgresletFullname                    string
		initDBJobCMName                        string
		tlsClusterIssuer                       string
		tlsSubDomain                           string
		walGExporterImage                      string
		walGExporterCPULimit                   string
		walGExporterMemoryLimit                string
		podAntiaffinityTopologyKey             string
		podTopologySpreadConstraintTopologyKey string

		enableLeaderElection                     bool
		enableCRDRegistration                    bool
		enableNetPol                             bool
		enablePodAntiaffinity                    bool
		enableStandbyLeaderSelector              bool
		enableLegacyStandbySelector              bool
		deployEtcd                               bool
		enableLBSourceRanges                     bool
		enableRandomStorageEncryptionSecret      bool
		enableWalGEncryption                     bool
		enableForceSharedIP                      bool
		enableBootstrapStandbyFromS3             bool
		enableSuperUserForDBO                    bool
		enablePatroniFailsafeMode                bool
		enableFsGroupChangePolicyWebhook         bool
		enableWalGExporter                       bool
		podAntiaffinityPreferredDuringScheduling bool
		enablePodTopologySpreadConstraintWebhook bool
		enableStandbyHostAttrs                   bool

		portRangeStart                        int32
		portRangeSize                         int32
		replicationChangeRequeueTimeInSeconds int
		podTopologySpreadConstraintMaxSkew    int32
		podTopologySpreadConstraintMinDomains int32

		patroniTTL          uint32
		patroniLoopWait     uint32
		patroniRetryTimeout uint32

		pgParamBlockList map[string]bool

		standbyClusterSourceRanges []string

		showVersion = flag.Bool("version", false, "dump current version and exit")
	)

	flag.Parse()
	if *showVersion {
		fmt.Println(v.V.String())
		os.Exit(0)
	}
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

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
			setupLog.Error(nil, fmt.Sprintf("Cannot parse provided %s %q, exiting.", loadBalancerIPFlg, lbIP))
			os.Exit(1)
		}
	}

	// todo: Check the default port range start and size.
	viper.SetDefault(portRangeStartFlg, 32000)
	portRangeStart = viper.GetInt32(portRangeStartFlg)
	viper.SetDefault(portRangeSizeFlg, 8000)
	portRangeSize = viper.GetInt32(portRangeSizeFlg)

	viper.SetDefault(customPSPNameFlg, "postgres-operator-psp")
	pspName = viper.GetString(customPSPNameFlg)

	storageClass = viper.GetString(storageClassFlg)

	operatorImage = viper.GetString(operatorImageFlg)
	postgresImage = viper.GetString(postgresImageFlg)

	etcdHost = viper.GetString(etcdHostFlg)

	viper.SetDefault(crdRegistrationFlg, true)
	enableCRDRegistration = viper.GetBool(crdRegistrationFlg)

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

	viper.SetDefault(podAntiaffinityPreferredDuringSchedulingFlg, false)
	podAntiaffinityPreferredDuringScheduling = viper.GetBool(podAntiaffinityPreferredDuringSchedulingFlg)

	podAntiaffinityTopologyKey = viper.GetString(podAntiaffinityTopologyKeyFlg)

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

	viper.SetDefault(replicationChangeRequeueTimeFlg, 10)
	replicationChangeRequeueTimeInSeconds = viper.GetInt(replicationChangeRequeueTimeFlg)
	replicationChangeRequeueDuration := time.Duration(replicationChangeRequeueTimeInSeconds) * time.Second

	viper.SetDefault(enableLBSourceRangesFlg, true)
	enableLBSourceRanges = viper.GetBool(enableLBSourceRangesFlg)

	viper.SetDefault(enableRandomStorageEncryptionSecretFlg, false)
	enableRandomStorageEncryptionSecret = viper.GetBool(enableRandomStorageEncryptionSecretFlg)

	viper.SetDefault(enableWalGEncryptionFlg, false)
	enableWalGEncryption = viper.GetBool(enableWalGEncryptionFlg)

	viper.SetDefault(enableForceSharedIPFlg, true) // TODO switch to false?
	enableForceSharedIP = viper.GetBool(enableForceSharedIPFlg)

	viper.SetDefault(initDBJobCMNameFlg, "postgreslet-postgres-initdbjob")
	initDBJobCMName = viper.GetString(initDBJobCMNameFlg)

	viper.SetDefault(enableBootstrapStandbyFromS3Flg, true)
	enableBootstrapStandbyFromS3 = viper.GetBool(enableBootstrapStandbyFromS3Flg)

	viper.SetDefault(enableSuperUserForDBOFlg, false)
	enableSuperUserForDBO = viper.GetBool(enableSuperUserForDBOFlg)

	tlsClusterIssuer = viper.GetString(tlsClusterIssuerFlg)
	enableCustomTLSCert := false
	if tlsClusterIssuer != "" {
		enableCustomTLSCert = true
	}
	tlsSubDomain = viper.GetString(tlsSubDomainFlg)

	viper.SetDefault(enablePatroniFailsafeModeFlg, true)
	enablePatroniFailsafeMode = viper.GetBool(enablePatroniFailsafeModeFlg)

	viper.SetDefault(enableFsGroupChangePolicyWebhookFlg, true)
	enableFsGroupChangePolicyWebhook = viper.GetBool(enableFsGroupChangePolicyWebhookFlg)

	viper.SetDefault(enableWalGExporterFlg, true)
	enableWalGExporter = viper.GetBool(enableWalGExporterFlg)

	viper.SetDefault(walGExporterImage, "ghcr.io/thedatabaseme/wal-g-exporter:0.3.1")
	walGExporterImage = viper.GetString(walGExporterImageFlg)

	viper.SetDefault(walGExporterCPULimitFlg, "500m")
	walGExporterCPULimit = viper.GetString(walGExporterCPULimitFlg)
	resource.MustParse(walGExporterCPULimit)

	viper.SetDefault(walGExporterMemoryLimit, "500M")
	walGExporterMemoryLimit = viper.GetString(walGExporterMemoryLimitFlg)
	resource.MustParse(walGExporterMemoryLimit)

	viper.SetDefault(enablePodTopologySpreadConstraintWebhookFlg, false)
	enablePodTopologySpreadConstraintWebhook = viper.GetBool(enablePodTopologySpreadConstraintWebhookFlg)
	viper.SetDefault(podTopologySpreadConstraintTopologyKeyFlg, "machine.metal-stack.io/rack")
	podTopologySpreadConstraintTopologyKey = viper.GetString(podTopologySpreadConstraintTopologyKeyFlg)
	viper.SetDefault(podTopologySpreadConstraintMaxSkewFlg, 1)
	podTopologySpreadConstraintMaxSkew = viper.GetInt32(podTopologySpreadConstraintMaxSkewFlg)
	podTopologySpreadConstraintMinDomains = viper.GetInt32(podTopologySpreadConstraintMinDomainsFlg)

	viper.SetDefault(enableStandbyHostAttrsFlg, false)
	enableStandbyHostAttrs = viper.GetBool(enableStandbyHostAttrsFlg)

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
		crdRegistrationFlg, enableCRDRegistration,
		pgParamBlockListFlg, pgParamBlockList,
		majorVersionUpgradeModeFlg, majorVersionUpgradeMode,
		standbyClustersSourceRangesFlg, standbyClusterSourceRanges,
		postgresletNamespaceFlg, postgresletNamespace,
		sidecarsCMNameFlg, sidecarsCMName,
		enableNetPolFlg, enableNetPol,
		enablePodAntiaffinityFlg, enablePodAntiaffinity,
		podAntiaffinityPreferredDuringSchedulingFlg, podAntiaffinityPreferredDuringScheduling,
		podAntiaffinityTopologyKeyFlg, podAntiaffinityTopologyKey,
		patroniRetryTimeoutFlg, patroniRetryTimeout,
		enableStandbyLeaderSelectorFlg, enableStandbyLeaderSelector,
		ControlPlaneNamespaceFlg, controlPlaneNamespace,
		enableLegacyStandbySelectorFlg, enableLegacyStandbySelector,
		deployEtcdFlg, deployEtcd,
		etcdImageFlg, etcdImage,
		etcdBackupSidecarImageFlg, etcdBackupSidecarImage,
		etcdBackupSecretNameFlg, etcdBackupSecretName,
		etcdPSPNameFlg, etcdPSPName,
		enableLBSourceRangesFlg, enableLBSourceRanges,
		enableRandomStorageEncryptionSecretFlg, enableRandomStorageEncryptionSecret,
		postgresletFullnameFlg, postgresletFullname,
		replicationChangeRequeueTimeFlg, replicationChangeRequeueTimeInSeconds,
		enableWalGEncryptionFlg, enableWalGEncryption,
		enableForceSharedIPFlg, enableForceSharedIP,
		initDBJobCMNameFlg, initDBJobCMName,
		enableBootstrapStandbyFromS3Flg, enableBootstrapStandbyFromS3,
		enableSuperUserForDBOFlg, enableSuperUserForDBO,
		tlsClusterIssuerFlg, tlsClusterIssuer,
		tlsSubDomainFlg, tlsSubDomain,
		enablePatroniFailsafeModeFlg, enablePatroniFailsafeMode,
		enableFsGroupChangePolicyWebhookFlg, enableFsGroupChangePolicyWebhook,
		enableWalGExporterFlg, enableWalGExporter,
		walGExporterImageFlg, walGExporterImage,
		walGExporterCPULimitFlg, walGExporterCPULimit,
		walGExporterMemoryLimitFlg, walGExporterMemoryLimit,
		enablePodTopologySpreadConstraintWebhookFlg, enablePodTopologySpreadConstraintWebhook,
		podTopologySpreadConstraintTopologyKeyFlg, podTopologySpreadConstraintTopologyKey,
		podTopologySpreadConstraintMaxSkewFlg, podTopologySpreadConstraintMaxSkew,
		podTopologySpreadConstraintMinDomainsFlg, podTopologySpreadConstraintMinDomains,
		enableStandbyHostAttrsFlg, enableStandbyHostAttrs,
	)

	svcClusterConf := ctrl.GetConfigOrDie()
	svcClusterMgr, err := ctrl.NewManager(svcClusterConf, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddrSvcMgr,
		},
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "908dd13e.fits.cloud",
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
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddrCtrlMgr,
		},
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "4d69ceab.fits.cloud",
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
		PspName:                                  pspName,
		OperatorImage:                            operatorImage,
		DockerImage:                              postgresImage,
		EtcdHost:                                 etcdHost,
		CRDRegistration:                          enableCRDRegistration,
		MajorVersionUpgradeMode:                  majorVersionUpgradeMode,
		PostgresletNamespace:                     postgresletNamespace,
		SidecarsConfigMapName:                    sidecarsCMName,
		PodAntiaffinity:                          enablePodAntiaffinity,
		PartitionID:                              partitionID,
		PatroniFailsafeMode:                      enablePatroniFailsafeMode,
		PodAntiaffinityPreferredDuringScheduling: podAntiaffinityPreferredDuringScheduling,
		PodAntiaffinityTopologyKey:               podAntiaffinityTopologyKey,
	}
	opMgr, err := operatormanager.New(svcClusterConf, "external/svc-postgres-operator.yaml", scheme, ctrl.Log.WithName("OperatorManager"), opMgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to create `OperatorManager`")
		os.Exit(1)
	}

	var lbMgrOpts lbmanager.Options = lbmanager.Options{
		LBIP:                        lbIP,
		PortRangeStart:              portRangeStart,
		PortRangeSize:               portRangeSize,
		EnableStandbyLeaderSelector: enableStandbyLeaderSelector,
		EnableLegacyStandbySelector: enableLegacyStandbySelector,
		StandbyClustersSourceRanges: standbyClusterSourceRanges,
		EnableLBSourceRanges:        enableLBSourceRanges,
		EnableForceSharedIP:         enableForceSharedIP,
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
		ReplicationChangeRequeueDuration:    replicationChangeRequeueDuration,
		EnableRandomStorageEncryptionSecret: enableRandomStorageEncryptionSecret,
		EnableWalGEncryption:                enableWalGEncryption,
		PostgresletFullname:                 postgresletFullname,
		PostgresImage:                       postgresImage,
		InitDBJobConfigMapName:              initDBJobCMName,
		EnableBootstrapStandbyFromS3:        enableBootstrapStandbyFromS3,
		EnableSuperUserForDBO:               enableSuperUserForDBO,
		EnableCustomTLSCert:                 enableCustomTLSCert,
		TLSClusterIssuer:                    tlsClusterIssuer,
		TLSSubDomain:                        tlsSubDomain,
		EnableWalGExporter:                  enableWalGExporter,
		WalGExporterImage:                   walGExporterImage,
		WalGExporterCPULimit:                walGExporterCPULimit,
		WalGExporterMemoryLimit:             walGExporterMemoryLimit,
		EnableStandbyHostAttrs:              enableStandbyHostAttrs,
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
		EnableForceSharedIP:   enableForceSharedIP,
	}).SetupWithManager(svcClusterMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Status")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	svcClusterMgr.GetWebhookServer().Register(
		"/mutate-v1-pod",
		&webhook.Admission{
			Handler: &webhooks.SpiloPodMutator{
				SvcClient:                                svcClusterMgr.GetClient(),
				Decoder:                                  admission.NewDecoder(svcClusterMgr.GetScheme()),
				Log:                                      ctrl.Log.WithName("webhooks").WithName("SpiloPodMutator"),
				EnableFsGroupChangePolicyWebhook:         enableFsGroupChangePolicyWebhook,
				EnablePodTopologySpreadConstraintWebhook: enablePodTopologySpreadConstraintWebhook,
				PodTopologySpreadConstraintTopologyKey:   podTopologySpreadConstraintTopologyKey,
				PodTopologySpreadConstraintMaxSkew:       podTopologySpreadConstraintMaxSkew,
				PodTopologySpreadConstraintMinDomains:    podTopologySpreadConstraintMinDomains,
			},
		},
	)

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
