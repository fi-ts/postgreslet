/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	firewall "github.com/metal-stack/firewall-controller/v2/api/v1"
	coreosv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
)

const (
	postgresExporterServiceName                    string = "postgres-exporter"
	postgresExporterServicePortName                string = "metrics"
	postgresExporterServicePortKeyName             string = "postgres-exporter-service-port"
	postgresExporterServiceTargetPortKeyName       string = "postgres-exporter-service-target-port"
	postgresExporterServiceTenantAnnotationName    string = pg.TenantLabelName
	postgresExporterServiceProjectIDAnnotationName string = pg.ProjectIDLabelName
	storageEncryptionKeyName                       string = "storage-encryption-key"
	storageEncryptionKeyFinalizerName              string = "postgres.database.fits.cloud/secret-finalizer"
	walGEncryptionSecretNamePostfix                string = "-walg-encryption"
	walGEncryptionSecretKeyName                    string = "key"
	podMonitorName                                 string = "patroni"
	walGExporterName                               string = "wal-g-exporter"
	walGExporterPort                               int32  = 9351
	podMonitorPort                                 string = "8008"
	initDBName                                     string = "postgres-initdb"
	initDBSQLDummy                                 string = `SELECT 'NOOP';`
	debugLogLevel                                  int    = 1
)

// requeue defines in how many seconds a requeue should happen
var requeue = ctrl.Result{
	Requeue: true,
}

// PostgresReconciler reconciles a Postgres object
type PostgresReconciler struct {
	CtrlClient                        client.Client
	SvcClient                         client.Client
	Log                               logr.Logger
	Scheme                            *runtime.Scheme
	PartitionID, Tenant, StorageClass string
	*operatormanager.OperatorManager
	*lbmanager.LBManager
	recorder                            record.EventRecorder
	PgParamBlockList                    map[string]bool
	StandbyClustersSourceRanges         []string
	PostgresletNamespace                string
	SidecarsConfigMapName               string
	EnableNetPol                        bool
	EtcdHost                            string
	PatroniTTL                          uint32
	PatroniLoopWait                     uint32
	PatroniRetryTimeout                 uint32
	ReplicationChangeRequeueDuration    time.Duration
	EnableRandomStorageEncryptionSecret bool
	EnableWalGEncryption                bool
	PostgresletFullname                 string
	PostgresImage                       string
	InitDBJobConfigMapName              string
	EnableBootstrapStandbyFromS3        bool
	EnableSuperUserForDBO               bool
	EnableCustomTLSCert                 bool
	TLSClusterIssuer                    string
	TLSSubDomain                        string
	EnableWalGExporter                  bool
	WalGExporterImage                   string
	WalGExporterCPULimit                string
	WalGExporterMemoryLimit             string
}

type PatroniStandbyCluster struct {
	CreateReplicaMethods []string `json:"create_replica_methods"`
	Host                 string   `json:"host"`
	Port                 int      `json:"port"`
	ApplicationName      string   `json:"application_name"`
}
type PatroniConfig struct {
	StandbyCluster             *PatroniStandbyCluster `json:"standby_cluster"`
	SynchronousNodesAdditional *string                `json:"synchronous_nodes_additional"`
}

// Reconcile is the entry point for postgres reconciliation.
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;list;watch
func (r *PostgresReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("pgID", req.NamespacedName.Name)

	instance := &pg.Postgres{}
	if err := r.CtrlClient.Get(ctx, req.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			// the instance was updated, but does not exist anymore -> do nothing, it was probably deleted
			log.Info("postgres already deleted")
			return ctrl.Result{}, nil
		}

		r.recorder.Eventf(instance, "Warning", "Error", "failed to get resource: %v", err)
		return ctrl.Result{}, err
	}
	log.V(debugLogLevel).Info("postgres fetched", "postgres", instance)

	log = log.WithValues("ns", instance.ToPeripheralResourceNamespace())

	if !r.isManagedByUs(instance) {
		log.V(debugLogLevel).Info("object should be managed by another postgreslet, ignored.")
		return ctrl.Result{}, nil
	}

	log.Info("reconciling")

	// Delete
	if instance.IsBeingDeleted() {
		instance.Status.Description = "Terminating"
		if err := r.CtrlClient.Status().Update(ctx, instance); err != nil {
			log.Error(err, "failed to update owner object")
			return ctrl.Result{}, err
		}
		log.Info("instance being deleted")

		matchingLabels := instance.ToZalandoPostgresqlMatchingLabels()
		namespace := instance.ToPeripheralResourceNamespace()

		if err := r.deleteCWNP(log, ctx, instance); client.IgnoreNotFound(err) != nil { // todo: remove ignorenotfound
			r.recorder.Event(instance, "Warning", "Error", "failed to delete ClusterwideNetworkPolicy")
			return ctrl.Result{}, err
		}
		log.V(debugLogLevel).Info("corresponding CRD ClusterwideNetworkPolicy deleted")

		if err := r.LBManager.DeleteSharedSvcLB(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Service with shared ip: %v", err)
			return ctrl.Result{}, err
		}

		if err := r.LBManager.DeleteDedicatedSvcLB(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Service with dedicated ip: %v", err)
			return ctrl.Result{}, err
		}
		log.V(debugLogLevel).Info("corresponding Service(s) of type LoadBalancer deleted")

		// delete the postgres-exporter service
		if err := r.deleteExporterSidecarService(log, ctx, namespace); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
		}

		if err := r.deleteZPostgresqlByLabels(log, ctx, matchingLabels, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Zalando resource: %v", err)
			return ctrl.Result{}, err
		}
		log.V(debugLogLevel).Info("owned zalando postgresql deleted")

		if err := r.deleteNetPol(ctx, instance); err != nil {
			log.Error(err, "failed to delete NetworkPolicy")
		} else {
			log.V(debugLogLevel).Info("corresponding NetworkPolicy deleted")
		}

		if err := r.removeStorageEncryptionSecretFinalizer(log, ctx, instance); err != nil {
			log.Error(err, "error while remnoving finalizer from storage encryption secret")
		} else {
			log.V(debugLogLevel).Info("finalizer from storage encryption secret removed")
		}

		deletable, err := r.OperatorManager.IsOperatorDeletable(ctx, namespace)
		if err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to check if the operator is idle: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while checking if the operator is idle: %w", err)
		}
		if !deletable {
			r.recorder.Event(instance, "Warning", "Self-Reconciliation", "operator not yet deletable, requeuing")
			log.Info("operator not yet deletable, requeuing")
			return ctrl.Result{Requeue: true}, nil
		}
		if err := r.OperatorManager.UninstallOperator(ctx, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to uninstall operator: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while uninstalling operator: %w", err)
		}
		log.V(debugLogLevel).Info("corresponding operator deleted")

		if err := r.deleteUserPasswordsSecret(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		log.V(debugLogLevel).Info("corresponding passwords secret deleted")

		instance.RemoveFinalizer(pg.PostgresFinalizerName)
		if err := r.CtrlClient.Update(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Self-Reconciliation", "failed to remove finalizer: %v", err)
			return ctrl.Result{}, fmt.Errorf("failed to update finalizers: %w", err)
		}
		log.V(debugLogLevel).Info("finalizers removed")
		log.Info("postgres deletion reconciled")
		return ctrl.Result{}, nil
	}

	// Add finalizer if none.
	if !instance.HasFinalizer(pg.PostgresFinalizerName) {
		instance.AddFinalizer(pg.PostgresFinalizerName)
		if err := r.CtrlClient.Update(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Self-Reconciliation", "failed to add finalizer: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while adding finalizer: %w", err)
		}
		log.V(debugLogLevel).Info("finalizer added")
	}

	// Check if zalando dependencies are installed. If not, install them.
	if err := r.ensureZalandoDependencies(log, ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to install operator: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while ensuring Zalando dependencies: %w", err)
	}

	// Add (or remove!) network policy
	if r.EnableNetPol {
		if err := r.createOrUpdateNetPol(ctx, instance, r.EtcdHost); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to create netpol: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while creating netpol: %w", err)
		}
	} else {
		if err := r.deleteNetPol(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete netpol: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while deleting netpol: %w", err)
		}
	}

	// Request certificate, if necessary
	if err := r.createOrUpdateCertificate(log, ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create certificate request: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating certificate request: %w", err)
	}

	// Make sure the postgres secrets exist, if necessary
	if err := r.ensurePostgresSecrets(log, ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create postgres secrets: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating postgres secrets: %w", err)
	}

	// check (and update if necessary) the current patroni replication config.
	requeueAfterReconcile, patroniConfigChangeErr := r.checkAndUpdatePatroniReplicationConfig(log, ctx, instance)

	// create standby egress rule first, so the standby can actually connect to the primary
	if err := r.createOrUpdateEgressCWNP(ctx, instance); err != nil {
		r.recorder.Event(instance, "Warning", "Error", "failed to create or update egress ClusterwideNetworkPolicy")
		return ctrl.Result{}, fmt.Errorf("unable to create or update egress ClusterwideNetworkPolicy: %w", err)
	}

	// try to fetch the global sidecars configmap
	cns := types.NamespacedName{
		Namespace: r.PostgresletNamespace,
		Name:      r.SidecarsConfigMapName,
	}
	globalSidecarsCM := &corev1.ConfigMap{}
	if err := r.SvcClient.Get(ctx, cns, globalSidecarsCM); err != nil {
		// configmap with configuration does not exists, nothing we can do here...
		return ctrl.Result{}, fmt.Errorf("could not fetch config for sidecars")
	}
	// Add services for our sidecars
	namespace := instance.ToPeripheralResourceNamespace()
	if err := r.createOrUpdateExporterSidecarServices(log, ctx, namespace, globalSidecarsCM, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating sidecars services %v: %w", namespace, err)
	}

	// Add service monitor for our exporter sidecar
	err := r.createOrUpdateExporterSidecarServiceMonitor(log, ctx, namespace, instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating sidecars servicemonitor %v: %w", namespace, err)
	}

	// Add pod monitor
	if err := r.createOrUpdatePatroniPodMonitor(ctx, namespace, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create podmonitor: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating podmonitor %v: %w", namespace, err)
	}

	// Make sure the storage secret exist, if necessary
	if err := r.ensureStorageEncryptionSecret(log, ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create storage secret: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating storage secret: %w", err)
	}

	if err := r.createOrUpdateZalandoPostgresql(ctx, instance, log, globalSidecarsCM, r.PatroniTTL, r.PatroniLoopWait, r.PatroniRetryTimeout); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create Zalando resource: %v", err)
		return ctrl.Result{}, fmt.Errorf("failed to create or update zalando postgresql: %w", err)
	}

	if err := r.ensureInitDBJob(log, ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create initDB job resource: %v", err)
		return ctrl.Result{}, fmt.Errorf("failed to create or update initdb job: %w", err)
	}

	if err := r.LBManager.ReconcileSvcLBs(ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create Service: %v", err)
		return ctrl.Result{}, err
	}

	if r.EnableWalGExporter {
		if err := r.createOrUpdateWalGExporterDeployment(log, ctx, namespace, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to deploy wal-g-exporter: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while deploying wal-g-exporter %v: %w", namespace, err)
		}
		if err := r.createOrUpdateWalGExporterPodMonitor(log, ctx, namespace, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to deploy wal-g-exporter podMonitor: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while deploying wal-g-exporter podMonitor %v: %w", namespace, err)
		}
	} else {
		// remove wal-g-exporter when disabled
		if err := r.deleteWalGExporterDeployment(ctx, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete wal-g-exporter: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while deleting wal-g-exporter: %w", err)
		}
		if err := r.deleteWalGExporterPodMonitor(ctx, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete wal-g-exporter podMonitor: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while deleting wal-g-exporter podMonitor: %w", err)
		}
	}

	// Check if socket port is ready
	port := instance.Status.Socket.Port
	if port == 0 {
		r.recorder.Event(instance, "Warning", "Self-Reconciliation", "socket port not ready")
		log.Info("socket port not ready, requeuing")
		return requeue, nil
	}

	// Update status will be handled by the StatusReconciler, based on the Zalando Status
	if err := r.createOrUpdateIngressCWNP(log, ctx, instance, int(port)); err != nil {
		r.recorder.Event(instance, "Warning", "Error", "failed to create or update ingress ClusterwideNetworkPolicy")
		return ctrl.Result{}, fmt.Errorf("unable to create or update ingress ClusterwideNetworkPolicy: %w", err)
	}

	// when an error occurred while updating the patroni config, requeue here
	// we try again in the next loop, hoping things will settle
	if patroniConfigChangeErr != nil {
		log.Info("Requeuing after getting/setting patroni replication config failed")
		return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, patroniConfigChangeErr
	}
	// if the config isn't in the expected state yet (we only add values to an existing config, we do not perform the actual switch), we simply requeue.
	// on the next reconciliation loop, postgres-operator should have caught up and the config should hopefully be correct already so we can continue with adding our values.
	if requeueAfterReconcile {
		log.Info("Requeuing after patroni replication hasn't returned the expected state (yet)")
		return ctrl.Result{Requeue: true, RequeueAfter: r.ReplicationChangeRequeueDuration}, nil
	}

	log.Info("postgres reconciled")
	r.recorder.Event(instance, "Normal", "Reconciled", "postgres up to date")
	return ctrl.Result{}, nil
}

// SetupWithManager informs mgr when this reconciler should be called.
func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("PostgresController")
	return ctrl.NewControllerManagedBy(mgr).
		For(&pg.Postgres{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

func (r *PostgresReconciler) createOrUpdateZalandoPostgresql(ctx context.Context, instance *pg.Postgres, log logr.Logger, sidecarsCM *corev1.ConfigMap, patroniTTL, patroniLoopWait, patroniRetryTimeout uint32) error {
	var restoreBackupConfig *pg.BackupConfig
	var restoreSourceInstance *pg.Postgres
	if instance.Spec.PostgresRestore != nil {
		if instance.Spec.PostgresRestore.SourcePostgresID == "" {
			return fmt.Errorf("restore requested, but no source configured")
		}
		srcNs := types.NamespacedName{
			Namespace: instance.Namespace,
			Name:      instance.Spec.PostgresRestore.SourcePostgresID,
		}
		src := &pg.Postgres{}
		if err := r.CtrlClient.Get(ctx, srcNs, src); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to get source postgres for restore: %v", err)
		} else {
			log.V(debugLogLevel).Info("source for restore fetched", "postgres", instance)

			bc, err := r.getBackupConfig(ctx, instance.Namespace, src.Spec.BackupSecretRef)
			if err != nil {
				return err
			}

			restoreBackupConfig = bc
			restoreSourceInstance = src
		}
	}

	// Get zalando postgresql and create one if none.
	rawZ, err := r.getZalandoPostgresql(ctx, instance)
	if err != nil {
		// errors other than `NotFound`
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to fetch zalando postgresql: %w", err)
		}

		u, err := instance.ToUnstructuredZalandoPostgresql(nil, sidecarsCM, r.StorageClass, r.PgParamBlockList, restoreBackupConfig, restoreSourceInstance, patroniTTL, patroniLoopWait, patroniRetryTimeout, r.EnableSuperUserForDBO, r.EnableCustomTLSCert)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
		}

		if err := r.SvcClient.Create(ctx, u); err != nil {
			return fmt.Errorf("failed to create zalando postgresql: %w", err)
		}
		log.V(debugLogLevel).Info("zalando postgresql created", "postgresql", u)

		return nil
	}

	// Update zalando postgresql
	mergeFrom := client.MergeFrom(rawZ.DeepCopy())

	u, err := instance.ToUnstructuredZalandoPostgresql(rawZ, sidecarsCM, r.StorageClass, r.PgParamBlockList, restoreBackupConfig, restoreSourceInstance, patroniTTL, patroniLoopWait, patroniRetryTimeout, r.EnableSuperUserForDBO, r.EnableCustomTLSCert)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
	}
	if err := r.SvcClient.Patch(ctx, u, mergeFrom); err != nil {
		return fmt.Errorf("failed to update zalando postgresql: %w", err)
	}
	log.V(debugLogLevel).Info("zalando postgresql updated", "postgresql", u)

	return nil
}

func (r *PostgresReconciler) deleteUserPasswordsSecret(ctx context.Context, instance *pg.Postgres) error {
	secret := &corev1.Secret{}
	secret.Namespace = instance.Namespace
	secret.Name = instance.ToUserPasswordsSecretName()
	if err := r.CtrlClient.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
		msgWithFormat := "failed to delete user passwords secret: %w"
		r.recorder.Eventf(instance, "Warning", "Error", msgWithFormat, err)
		return fmt.Errorf(msgWithFormat, err)
	}

	return nil
}

// ensureZalandoDependencies makes sure Zalando resources are installed in the service-cluster.
func (r *PostgresReconciler) ensureZalandoDependencies(log logr.Logger, ctx context.Context, p *pg.Postgres) error {
	namespace := p.ToPeripheralResourceNamespace()
	isInstalled, err := r.OperatorManager.IsOperatorInstalled(ctx, namespace)
	if err != nil {
		return fmt.Errorf("error while querying if zalando dependencies are installed: %w", err)
	}

	if !isInstalled {
		if err := r.OperatorManager.InstallOrUpdateOperator(ctx, namespace); err != nil {
			return fmt.Errorf("error while installing zalando dependencies: %w", err)
		}
	}

	if err := r.updatePodEnvironmentConfigMap(log, ctx, p); err != nil {
		return fmt.Errorf("error while updating backup config: %w", err)
	}

	if err := r.updatePodEnvironmentSecret(log, ctx, p); err != nil {
		return fmt.Errorf("error while updating backup config secret: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) updatePodEnvironmentConfigMap(log logr.Logger, ctx context.Context, p *pg.Postgres) error {
	if p.Spec.BackupSecretRef == "" {
		log.Info("No configured backupSecretRef found, skipping configuration of postgres backup")
		return nil
	}

	backupConfig, err := r.getBackupConfig(ctx, p.Namespace, p.Spec.BackupSecretRef)
	if err != nil {
		return err
	}

	s3url, err := url.Parse(backupConfig.S3Endpoint)
	if err != nil {
		return fmt.Errorf("error while parsing the s3 endpoint url in the backup secret: %w", err)
	}
	// use the s3 endpoint as provided
	awsEndpoint := s3url.String()
	// modify the scheme to 'https+path'
	s3url.Scheme = "https+path"
	// use the modified s3 endpoint
	walES3Endpoint := s3url.String()
	// region
	region := backupConfig.S3Region

	// set the WALG_UPLOAD_DISK_CONCURRENCY based on the configured cpu limits
	q, err := resource.ParseQuantity(p.Spec.Size.CPU)
	if err != nil {
		return fmt.Errorf("error while parsing the postgres cpu size: %w", err)
	}
	uploadDiskConcurrency := "1"
	if q.Value() > 32 {
		uploadDiskConcurrency = "32"
	} else if q.Value() > 1 {
		uploadDiskConcurrency = fmt.Sprint(q.Value())
	}
	uploadConcurrency := "32"
	downloadConcurrency := "32"

	// use the rest as provided in the secret
	bucketName := backupConfig.S3BucketName
	backupSchedule := backupConfig.Schedule
	backupNumToRetain := backupConfig.Retention

	// s3 server side encryption SSE is disabled
	// we use client side encryption
	walgDisableSSE := "true"

	// create updated content for pod environment configmap
	data := map[string]string{
		"USE_WALG_BACKUP":                    "true",
		"USE_WALG_RESTORE":                   "true",
		"WALE_S3_PREFIX":                     "s3://" + bucketName + "/$(SCOPE)",
		"WALG_S3_PREFIX":                     "s3://" + bucketName + "/$(SCOPE)",
		"CLONE_WALG_S3_PREFIX":               "s3://" + bucketName + "/$(CLONE_SCOPE)",
		"WALE_BACKUP_THRESHOLD_PERCENTAGE":   "100",
		"AWS_ENDPOINT":                       awsEndpoint,
		"WALE_S3_ENDPOINT":                   walES3Endpoint, // same as above, but slightly modified
		"AWS_S3_FORCE_PATH_STYLE":            "true",
		"AWS_REGION":                         region,         // now we can use AWS S3
		"WALG_DISABLE_S3_SSE":                walgDisableSSE, // server side encryption
		"BACKUP_SCHEDULE":                    backupSchedule,
		"BACKUP_NUM_TO_RETAIN":               backupNumToRetain,
		"WALG_UPLOAD_DISK_CONCURRENCY":       uploadDiskConcurrency,
		"CLONE_WALG_UPLOAD_DISK_CONCURRENCY": uploadDiskConcurrency,
		"WALG_UPLOAD_CONCURRENCY":            uploadConcurrency,
		"CLONE_WALG_UPLOAD_CONCURRENCY":      uploadConcurrency,
		"WALG_DOWNLOAD_CONCURRENCY":          downloadConcurrency,
		"CLONE_WALG_DOWNLOAD_CONCURRENCY":    downloadConcurrency,
	}

	if r.EnableBootstrapStandbyFromS3 && p.IsReplicationTarget() {
		data["STANDBY_WALG_UPLOAD_DISK_CONCURRENCY"] = uploadDiskConcurrency
		data["STANDBY_WALG_UPLOAD_CONCURRENCY"] = uploadConcurrency
		data["STANDBY_WALG_DOWNLOAD_CONCURRENCY"] = downloadConcurrency
	}

	cm := &corev1.ConfigMap{}
	ns := types.NamespacedName{
		Name:      operatormanager.PodEnvCMName,
		Namespace: p.ToPeripheralResourceNamespace(),
	}
	if err := r.SvcClient.Get(ctx, ns, cm); err != nil {
		// when updating from v0.7.0 straight to v0.10.0, we neither have that ConfigMap (as we use a Secret in version
		// v0.7.0) nor do we create it (the new labels aren't there yet, so the selector does not match and
		// operatormanager.OperatorManager.UpdateAllManagedOperators does not call InstallOrUpdateOperator)
		// we previously aborted here (before the postgresql resource was updated with the new labels), meaning we would
		// simply restart the loop without solving the problem.
		if cm, err = r.OperatorManager.CreatePodEnvironmentConfigMap(ctx, ns.Namespace); err != nil {
			return fmt.Errorf("error while creating the missing Pod Environment ConfigMap %v: %w", ns.Namespace, err)
		}
		log.Info("missing Pod Environment ConfigMap created!")
	}
	cm.Data = data
	if err := r.SvcClient.Update(ctx, cm); err != nil {
		return fmt.Errorf("error while updating the pod environment configmap in service cluster: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) updatePodEnvironmentSecret(log logr.Logger, ctx context.Context, p *pg.Postgres) error {
	if p.Spec.BackupSecretRef == "" {
		log.Info("No configured backupSecretRef found, skipping configuration of postgres backup")
		return nil
	}

	backupConfig, err := r.getBackupConfig(ctx, p.Namespace, p.Spec.BackupSecretRef)
	if err != nil {
		return err
	}

	awsAccessKeyID := backupConfig.S3AccessKey
	awsSecretAccessKey := backupConfig.S3SecretKey

	// create updated content for pod environment configmap
	data := map[string][]byte{
		"AWS_ACCESS_KEY_ID":     []byte(awsAccessKeyID),
		"AWS_SECRET_ACCESS_KEY": []byte(awsSecretAccessKey),
	}

	// libsodium client side encryption key
	if r.EnableWalGEncryption {
		s, err := r.getWalGEncryptionSecret(ctx)
		if err != nil {
			return err
		}
		k, exists := s.Data[walGEncryptionSecretKeyName]
		if !exists {
			return fmt.Errorf("could not find key %v in secret %v/%v-%v", walGEncryptionSecretKeyName, r.PostgresletNamespace, r.PostgresletFullname, walGEncryptionSecretNamePostfix)
		}
		// libsodium keys are fixed-size keys of 32 bytes, see https://github.com/wal-g/wal-g#encryption
		if len(k) != 32 {
			return fmt.Errorf("wal_g encryption key must be exactly 32 bytes, got %v", len(k))
		}
		data["WALG_LIBSODIUM_KEY"] = k

		if p.Spec.PostgresRestore != nil {
			data["CLONE_WALG_LIBSODIUM_KEY"] = k
		} else {
			delete(data, "CLONE_WALG_LIBSODIUM_KEY")
		}

		// we also need that (hopefully identical) key to bootstrap from files in S3
		if r.EnableBootstrapStandbyFromS3 && p.IsReplicationTarget() {
			data["STANDBY_WALG_LIBSODIUM_KEY"] = k
		}
	}

	// add STANDBY_* variables for bootstrapping from S3
	if r.EnableBootstrapStandbyFromS3 && p.IsReplicationTarget() {
		standbyEnvs := r.getStandbyEnvs(ctx, p)
		for name, value := range standbyEnvs {
			data[name] = value
		}
	}

	var s *corev1.Secret
	ns := p.ToPeripheralResourceNamespace()

	if s, err = r.OperatorManager.CreateOrGetPodEnvironmentSecret(ctx, ns); err != nil {
		return fmt.Errorf("error while accessing the pod environment secret %v: %w", ns, err)
	}

	s.Data = data
	if err := r.SvcClient.Update(ctx, s); err != nil {
		return fmt.Errorf("error while updating the pod environment secret in service cluster: %w", err)
	}

	return nil
}

// getStandbyEnvs Fetches all the required info from the remote primary postgres and fills all ENVS required for bootstrapping from S3
func (r *PostgresReconciler) getStandbyEnvs(ctx context.Context, p *pg.Postgres) map[string][]byte {
	standbyEnvs := map[string][]byte{}

	// fetch backup secret of primary
	primary := &pg.Postgres{}
	ns := types.NamespacedName{
		Name:      p.Spec.PostgresConnection.ConnectedPostgresID,
		Namespace: p.Namespace,
	}
	if err := r.CtrlClient.Get(ctx, ns, primary); err != nil {
		if apierrors.IsNotFound(err) {
			// the instance was updated, but does not exist anymore -> do nothing, it was probably deleted
			return standbyEnvs
		}

		r.recorder.Eventf(primary, "Warning", "Error", "failed to get referenced primary postgres: %v", err)
		return standbyEnvs
	}

	if primary.Spec.BackupSecretRef == "" {
		r.recorder.Eventf(primary, "Warning", "Error", "No backupSecretRef for primary postgres found, skipping configuration of wal_e bootstrapping")
		return standbyEnvs
	}

	primaryBackupConfig, err := r.getBackupConfig(ctx, primary.Namespace, primary.Spec.BackupSecretRef)
	if err != nil {
		r.recorder.Eventf(primary, "Warning", "Error", "failed to get referenced primary backup config, skipping configuration of wal_e bootstrapping: %v", err)
		return standbyEnvs
	}
	primaryS3url, err := url.Parse(primaryBackupConfig.S3Endpoint)
	if err != nil {
		r.recorder.Eventf(primary, "Warning", "Error", "error while parsing the s3 endpoint url in the backup secret: %w", err)
		return standbyEnvs
	}

	// use the s3 endpoint as provided
	primaryAwsEndpoint := primaryS3url.String()
	// modify the scheme to 'https+path'
	primaryS3url.Scheme = "https+path"
	// use the modified s3 endpoint
	primaryWalES3Endpoint := primaryS3url.String()
	// region
	primaryRegion := primaryBackupConfig.S3Region
	// s3 prefix
	primaryWalGS3Prefix := "s3://" + primaryBackupConfig.S3BucketName + "/" + primary.ToPeripheralResourceName()
	// s3 server side encryption SSE is disabled, we use client side encryption
	// see STANDBY_WALG_LIBSODIUM_KEY above
	primaryWalgDisableSSE := "true"
	// aws access key
	primaryAwsAccessKeyID := primaryBackupConfig.S3AccessKey
	// aws secret key
	primaryAwsSecretAccessKey := primaryBackupConfig.S3SecretKey

	// create updated content for pod environment configmap
	// this is a bit confusing: those are used to bootstrap a remote standby, so they have to point to the primary!
	standbyEnvs["STANDBY_AWS_ACCESS_KEY_ID"] = []byte(primaryAwsAccessKeyID)
	standbyEnvs["STANDBY_AWS_SECRET_ACCESS_KEY"] = []byte(primaryAwsSecretAccessKey)
	standbyEnvs["STANDBY_AWS_ENDPOINT"] = []byte(primaryAwsEndpoint)
	standbyEnvs["STANDBY_AWS_S3_FORCE_PATH_STYLE"] = []byte("true")
	standbyEnvs["STANDBY_AWS_REGION"] = []byte(primaryRegion)
	standbyEnvs["STANDBY_AWS_WALG_S3_ENDPOINT"] = []byte(primaryWalES3Endpoint)
	standbyEnvs["STANDBY_USE_WALG_BACKUP"] = []byte("true")
	standbyEnvs["STANDBY_USE_WALG_RESTORE"] = []byte("true")
	standbyEnvs["STANDBY_WALE_S3_ENDPOINT"] = []byte(primaryWalES3Endpoint)
	standbyEnvs["STANDBY_WALG_DISABLE_S3_SSE"] = []byte(primaryWalgDisableSSE)
	standbyEnvs["STANDBY_WALG_S3_ENDPOINT"] = []byte(primaryWalES3Endpoint)
	standbyEnvs["STANDBY_WALG_S3_PREFIX"] = []byte(primaryWalGS3Prefix)
	standbyEnvs["STANDBY_WALG_S3_SSE"] = []byte("")
	standbyEnvs["STANDBY_WITH_WALG"] = []byte("true")

	return standbyEnvs
}

func (r *PostgresReconciler) isManagedByUs(obj *pg.Postgres) bool {
	if obj.Spec.PartitionID != r.PartitionID {
		return false
	}

	// if this partition is only for one tenant
	if r.Tenant != "" && obj.Spec.Tenant != r.Tenant {
		return false
	}

	return true
}

func (r *PostgresReconciler) deleteZPostgresqlByLabels(log logr.Logger, ctx context.Context, matchingLabels client.MatchingLabels, namespace string) error {

	items, err := r.getZPostgresqlByLabels(ctx, matchingLabels, namespace)
	if err != nil {
		return err
	}

	for i, rawZ := range items {
		if err := r.SvcClient.Delete(ctx, &items[i]); err != nil {
			return fmt.Errorf("error while deleting zalando postgresql: %w", err)
		}
		log.V(debugLogLevel).Info("zalando postgresql deleted", "postgresql", rawZ)
	}

	return nil
}

// todo: Change to `controllerutl.CreateOrPatch`
// createOrUpdateIngressCWNP will create an ingress firewall rule on the firewall in front of the k8s cluster
// based on the spec.AccessList.SourceRanges and pre-configured standby clusters source ranges sgiven.
func (r *PostgresReconciler) createOrUpdateIngressCWNP(log logr.Logger, ctx context.Context, in *pg.Postgres, port int) error {
	policy, err := in.ToCWNP(port)
	if err != nil {
		return fmt.Errorf("unable to convert instance to CRD ClusterwideNetworkPolicy: %w", err)
	}

	// placeholder of the object with the specified namespaced name
	key := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: policy.Name, Namespace: policy.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, key, func() error {
		key.Spec.Ingress = policy.Spec.Ingress
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy CRD ClusterwideNetworkPolicy: %w", err)
	}
	log.V(debugLogLevel).Info("clusterwidenetworkpolicy created or updated")

	if in.Spec.PostgresConnection == nil {
		// abort if there are no connected postgres instances
		return nil
	}

	// Create CWNP if standby is configured (independent of the current role)

	standbyIngressCWNP, err := in.ToStandbyClusterIngressCWNP(r.StandbyClustersSourceRanges)
	if err != nil {
		return fmt.Errorf("unable to convert instance to standby ingress ClusterwideNetworkPolicy: %w", err)
	}

	key2 := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyIngressCWNP.Name, Namespace: standbyIngressCWNP.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, key2, func() error {
		key2.Spec.Ingress = standbyIngressCWNP.Spec.Ingress
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy standby ingress ClusterwideNetworkPolicy: %w", err)
	}

	return nil
}

// todo: Change to `controllerutl.CreateOrPatch`
// createOrUpdateEgressCWNP will create an egress firewall rule on the firewall in front of the k8s cluster
// based on the spec.PostgresConnection.
func (r *PostgresReconciler) createOrUpdateEgressCWNP(ctx context.Context, in *pg.Postgres) error {

	if in.Spec.PostgresConnection == nil {
		// abort if there are no connected postgres instances
		return nil
	}

	//
	// Create egress rule to StandbyCluster CIDR and ConnectionPort
	//
	standbyEgressCWNP, err := in.ToStandbyClusterEgressCWNP()
	if err != nil {
		return fmt.Errorf("unable to convert instance to standby egress ClusterwideNetworkPolicy: %w", err)
	}

	key3 := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyEgressCWNP.Name, Namespace: standbyEgressCWNP.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, key3, func() error {
		key3.Spec.Egress = standbyEgressCWNP.Spec.Egress
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy standby egress ClusterwideNetworkPolicy: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) deleteCWNP(log logr.Logger, ctx context.Context, in *pg.Postgres) error {
	stdbyIngresPolicy := &firewall.ClusterwideNetworkPolicy{}
	stdbyIngresPolicy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	stdbyIngresPolicy.Name = in.ToStandbyClusterIngresCWNPName()
	if err := r.SvcClient.Delete(ctx, stdbyIngresPolicy); err != nil {
		log.V(debugLogLevel).Info("could not delete standby cluster ingress policy")
	}

	stdbyEgresPolicy := &firewall.ClusterwideNetworkPolicy{}
	stdbyEgresPolicy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	stdbyEgresPolicy.Name = in.ToStandbyClusterEgresCWNPName()
	if err := r.SvcClient.Delete(ctx, stdbyEgresPolicy); err != nil {
		log.V(debugLogLevel).Info("could not delete standby cluster egress policy")
	}

	policy := &firewall.ClusterwideNetworkPolicy{}
	policy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	policy.Name = in.ToPeripheralResourceName()
	if err := r.SvcClient.Delete(ctx, policy); err != nil {
		return fmt.Errorf("unable to delete CRD ClusterwideNetworkPolicy %v: %w", policy.Name, err)
	}

	return nil
}

// Returns *only one* Zalndo Postgresql resource with the given label, returns an error if not unique.
func (r *PostgresReconciler) getZalandoPostgresql(ctx context.Context, instance *pg.Postgres) (*zalando.Postgresql, error) {
	matchingLabels := instance.ToZalandoPostgresqlMatchingLabels()
	namespace := instance.ToPeripheralResourceNamespace()

	items, err := r.getZPostgresqlByLabels(ctx, matchingLabels, namespace)
	if err != nil {
		return nil, err
	}

	if len := len(items); len > 1 {
		return nil, fmt.Errorf("error while fetching zalando postgresql: Not unique, got %d results", len)
	} else if len < 1 {
		return nil, apierrors.NewNotFound(zalando.Resource("postgresql"), "")
	}

	return &items[0], nil
}

// Fetches all the Zalando Postgreql resources from the service cluster that have the given labels attached.
func (r *PostgresReconciler) getZPostgresqlByLabels(ctx context.Context, matchingLabels client.MatchingLabels, namespace string) ([]zalando.Postgresql, error) {

	zpl := &zalando.PostgresqlList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		matchingLabels,
	}
	if err := r.SvcClient.List(ctx, zpl, opts...); err != nil {
		return nil, err
	}

	return zpl.Items, nil
}

func (r *PostgresReconciler) ensurePostgresSecrets(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {

	if err := r.ensureStandbySecrets(log, ctx, instance); err != nil {
		return err
	}

	if err := r.ensureCloneSecrets(log, ctx, instance); err != nil {
		return err
	}

	return nil

}

func (r *PostgresReconciler) ensureStandbySecrets(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {
	if instance.IsReplicationPrimaryOrStandalone() {
		// nothing is configured, or we are the leader. nothing to do.
		return nil
	}

	//  Check if instance.Spec.PostgresConnectionInfo.ConnectionSecretName is defined
	if instance.Spec.PostgresConnection.ConnectionSecretName == "" {
		return errors.New("connectionInfo.secretName not configured")
	}

	// Check if secret for standby user exist local in SERVICE Cluster
	localStandbySecretName := pg.PostgresConfigReplicationUsername + "." + instance.ToPeripheralResourceName() + ".credentials"
	localSecretNamespace := instance.ToPeripheralResourceNamespace()
	localStandbySecret := &corev1.Secret{}
	log.V(debugLogLevel).Info("checking for local standby secret", "name", localStandbySecretName)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: localSecretNamespace, Name: localStandbySecretName}, localStandbySecret)

	if err == nil {
		log.V(debugLogLevel).Info("local standby secret found, checking for monitoring secret next")
	} else if !apierrors.IsNotFound(err) {
		// we got an error other than not found, so we cannot continue!
		return fmt.Errorf("error while fetching local standby secret from service cluster: %w", err)
	}

	// Check if secret for monitoring user exist local in SERVICE Cluster
	localMonitoringSecretName := pg.PostgresConfigMonitoringUsername + "." + instance.ToPeripheralResourceName() + ".credentials"
	localSecretNamespace = instance.ToPeripheralResourceNamespace()
	localStandbySecret = &corev1.Secret{}
	log.V(debugLogLevel).Info("checking for local monitoring secret", "name", localMonitoringSecretName)
	err = r.SvcClient.Get(ctx, types.NamespacedName{Namespace: localSecretNamespace, Name: localMonitoringSecretName}, localStandbySecret)

	if err == nil {
		log.V(debugLogLevel).Info("local monitoring secret found, no action needed")
		return nil
	} else if !apierrors.IsNotFound(err) {
		// we got an error other than not found, so we cannot continue!
		return fmt.Errorf("error while fetching local monitoring secret from service cluster: %w", err)
	}

	log.Info("not all expected local secrets found, continuing to create them")

	remoteSecretNamespacedName := types.NamespacedName{
		Namespace: instance.ObjectMeta.Namespace,
		Name:      instance.Spec.PostgresConnection.ConnectionSecretName,
	}
	return r.copySecrets(log, ctx, remoteSecretNamespacedName, instance, false)

}

func (r *PostgresReconciler) ensureCloneSecrets(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {
	if instance.Spec.PostgresRestore == nil {
		// not a clone. nothing to do.
		return nil
	}

	//  Check if instance.Spec.PostgresConnectionInfo.ConnectionSecretName is defined
	if instance.Spec.PostgresRestore.SourcePostgresID == "" {
		return errors.New("SourcePostgresID not configured")
	}

	// Check if secrets exist local in SERVICE Cluster
	localStandbySecretName := pg.PostresConfigSuperUsername + "." + instance.ToPeripheralResourceName() + ".credentials"
	localSecretNamespace := instance.ToPeripheralResourceNamespace()
	localStandbySecret := &corev1.Secret{}
	log.V(debugLogLevel).Info("checking for local postgres secret", "name", localStandbySecretName)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: localSecretNamespace, Name: localStandbySecretName}, localStandbySecret)

	if err == nil {
		log.V(debugLogLevel).Info("local postgres secret found, no action needed")
		return nil
	}

	// we got an error other than not found, so we cannot continue!
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("error while fetching local stadnby secret from service cluster: %w", err)
	}

	log.Info("no local postgres secret found, continuing to create one")

	remoteSecretName := strings.Replace(instance.ToUserPasswordsSecretName(), instance.Name, instance.Spec.PostgresRestore.SourcePostgresID, 1) // TODO this is hacky-wacky...
	remoteSecretNamespacedName := types.NamespacedName{
		Namespace: instance.ObjectMeta.Namespace,
		Name:      remoteSecretName,
	}
	return r.copySecrets(log, ctx, remoteSecretNamespacedName, instance, true)

}

func (r *PostgresReconciler) copySecrets(log logr.Logger, ctx context.Context, sourceSecret types.NamespacedName, targetInstance *pg.Postgres, ignoreStandbyUser bool) error {
	// Check if secrets exist in remote CONTROL Cluster
	remoteSecret := &corev1.Secret{}
	log.V(debugLogLevel).Info("fetching remote postgres secret", "src ns", sourceSecret.Namespace, "src name", sourceSecret.Name)
	if err := r.CtrlClient.Get(ctx, sourceSecret, remoteSecret); err != nil {
		// we cannot read the secret given in the configuration, so we cannot continue!
		return fmt.Errorf("error while fetching remote postgres secret from control plane: %w", err)
	}

	// copy all but the standby secrets...
	for username := range remoteSecret.Data {
		// check if we skip the standby user (e.g. to prevent old standby instances from connecting once a clone took over its sources ip/port)
		if ignoreStandbyUser && username == pg.PostgresConfigReplicationUsername {
			continue
		}

		log.Info("creating local secret", "username", username)

		currentSecretName := strings.ReplaceAll(username, "_", "-") + "." + targetInstance.ToPeripheralResourceName() + ".credentials"
		postgresSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      currentSecretName,
				Namespace: targetInstance.ToPeripheralResourceNamespace(),
				Labels:    map[string]string(targetInstance.ToZalandoPostgresqlMatchingLabels()),
			},
			Data: map[string][]byte{
				"username": []byte(username),
				"password": remoteSecret.Data[username],
			},
		}

		if err := r.SvcClient.Create(ctx, postgresSecret); err != nil {
			if apierrors.IsAlreadyExists(err) {
				log.Info("local postgres secret already exists, skipping", "name", currentSecretName)
				continue
			}
			return fmt.Errorf("error while creating local secrets in service cluster: %w", err)
		}
	}

	return nil
}

func (r *PostgresReconciler) checkAndUpdatePatroniReplicationConfig(log logr.Logger, ctx context.Context, instance *pg.Postgres) (bool, error) {

	log = log.WithValues("label", "patroni")

	const requeueAfterReconcile = true
	const allDone = false

	log.V(debugLogLevel).Info("Checking replication config from Patroni API")

	// Get the leader pod
	leaderPods, err := r.findLeaderPods(log, ctx, instance)
	if err != nil {
		log.V(debugLogLevel).Info("could not query pods, requeuing")
		return requeueAfterReconcile, err
	}

	if len(leaderPods.Items) != 1 {
		log.V(debugLogLevel).Info("expected exactly one leader pod, selecting all spilo pods as a last resort (might be ok if it is still creating)")
		// To make sure any updates to the Zalando postgresql manifest are written, we do not requeue in this case
		return requeueAfterReconcile, r.updatePatroniReplicationConfigOnAllPods(log, ctx, instance)
	}
	leaderIP := leaderPods.Items[0].Status.PodIP

	// If there is no connected postgres, we still need to possibly clean up a former synchronous primary
	if instance.Spec.PostgresConnection == nil {
		log.V(debugLogLevel).Info("single instance, updating with empty config and requeing")
		return allDone, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
	}

	var resp *PatroniConfig
	resp, err = r.httpGetPatroniConfig(log, ctx, leaderIP)
	if err != nil {
		log.V(debugLogLevel).Info("could not query patroni, requeuing")
		return requeueAfterReconcile, err
	}
	if resp == nil {
		log.V(debugLogLevel).Info("got nil response from patroni, requeuing")
		return requeueAfterReconcile, nil
	}

	if instance.IsReplicationPrimaryOrStandalone() {
		if resp.StandbyCluster != nil {
			log.V(debugLogLevel).Info("standby_cluster mismatch, requeing", "response", resp)
			return requeueAfterReconcile, nil
		}
		if instance.Spec.PostgresConnection.SynchronousReplication {
			// fetch the sync standby to determine the correct application_name of the instance
			log.V(debugLogLevel).Info("fetching the referenced sync standby")
			var synchronousStandbyApplicationName *string
			s := &pg.Postgres{}
			ns := types.NamespacedName{
				Name:      instance.Spec.PostgresConnection.ConnectedPostgresID,
				Namespace: instance.Namespace,
			}
			if err := r.CtrlClient.Get(ctx, ns, s); err != nil {
				r.recorder.Eventf(s, "Warning", "Error", "failed to get referenced sync standby: %v", err)
				synchronousStandbyApplicationName = nil
			} else {
				synchronousStandbyApplicationName = ptr.To(s.ToPeripheralResourceName())
			}
			// compare the actual value with the expected value
			if synchronousStandbyApplicationName == nil {
				log.V(debugLogLevel).Info("could not fetch synchronous_nodes_additional, disabling sync replication and requeing", "response", resp)
				return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
			} else if resp.SynchronousNodesAdditional == nil || *resp.SynchronousNodesAdditional != *synchronousStandbyApplicationName {
				log.V(debugLogLevel).Info("synchronous_nodes_additional mismatch, updating and requeing", "response", resp)
				return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, synchronousStandbyApplicationName)
			}
		} else {
			if resp.SynchronousNodesAdditional != nil {
				log.V(debugLogLevel).Info("synchronous_nodes_additional mismatch, updating and requeing", "response", resp)
				return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
			}
		}

	} else {
		if resp.StandbyCluster == nil {
			log.V(debugLogLevel).Info("standby_cluster mismatch, requeing", "response", resp)
			return requeueAfterReconcile, nil
		}
		if resp.StandbyCluster.CreateReplicaMethods == nil {
			log.V(debugLogLevel).Info("create_replica_methods mismatch, updating and requeing", "response", resp)
			return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
		}
		if resp.StandbyCluster.Host != instance.Spec.PostgresConnection.ConnectionIP {
			log.V(debugLogLevel).Info("host mismatch, updating and requeing", "updating", resp)
			return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
		}
		if resp.StandbyCluster.Port != int(instance.Spec.PostgresConnection.ConnectionPort) {
			log.V(debugLogLevel).Info("port mismatch, updating and requeing", "updating", resp)
			return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
		}
		if resp.StandbyCluster.ApplicationName != instance.ToPeripheralResourceName() {
			log.V(debugLogLevel).Info("application_name mismatch, updating and requeing", "response", resp)
			return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
		}
		if resp.SynchronousNodesAdditional != nil {
			log.V(debugLogLevel).Info("synchronous_nodes_additional mismatch, updating and requeing", "response", resp)
			return requeueAfterReconcile, r.httpPatchPatroni(log, ctx, instance, leaderIP, nil)
		}
	}

	log.V(debugLogLevel).Info("replication config from Patroni API up to date")
	return allDone, nil
}

func (r *PostgresReconciler) findLeaderPods(log logr.Logger, ctx context.Context, instance *pg.Postgres) (*corev1.PodList, error) {
	leaderPods := &corev1.PodList{}
	roleReq, err := labels.NewRequirement(pg.SpiloRoleLabelName, selection.In, []string{pg.SpiloRoleLabelValueMaster, pg.SpiloRoleLabelValueStandbyLeader})
	if err != nil {
		log.V(debugLogLevel).Info("could not create requirements for label selector to query pods, requeuing")
		return leaderPods, err
	}
	leaderSelector := labels.NewSelector().Add(*roleReq)
	opts := []client.ListOption{
		client.InNamespace(instance.ToPeripheralResourceNamespace()),
		client.MatchingLabelsSelector{Selector: leaderSelector},
	}
	return leaderPods, r.SvcClient.List(ctx, leaderPods, opts...)
}

func (r *PostgresReconciler) updatePatroniReplicationConfigOnAllPods(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(instance.ToPeripheralResourceNamespace()),
		client.MatchingLabels{pg.ApplicationLabelName: pg.ApplicationLabelValue},
	}
	if err := r.SvcClient.List(ctx, pods, opts...); err != nil {
		log.V(debugLogLevel).Info("could not query pods, requeuing")
		return err
	}

	if len(pods.Items) == 0 {
		log.V(debugLogLevel).Info("no spilo pods found at all, requeuing")
		return errors.New("no spilo pods found at all")
	} else if len(pods.Items) < int(instance.Spec.NumberOfInstances) {
		log.V(debugLogLevel).Info("unexpected number of pods (might be ok if it is still creating)")
	}

	// iterate all spilo pods
	var lastErr error
	for _, pod := range pods.Items {
		pod := pod // pin!
		podIP := pod.Status.PodIP
		if err := r.httpPatchPatroni(log, ctx, instance, podIP, nil); err != nil {
			lastErr = err
			log.Info("failed to update pod")
		}
	}
	if lastErr != nil {
		log.V(debugLogLevel).Info("updating patroni config failed, got one or more errors")
		return lastErr
	}
	log.V(debugLogLevel).Info("updating patroni config succeeded")
	return nil
}

func (r *PostgresReconciler) httpPatchPatroni(log logr.Logger, ctx context.Context, instance *pg.Postgres, podIP string, synchronousStandbyApplicationName *string) error {
	if podIP == "" {
		return errors.New("podIP must not be empty")
	}

	podPort := "8008"
	path := "config"

	log.V(debugLogLevel).Info("Preparing request")
	var request PatroniConfig
	if instance.Spec.PostgresConnection == nil {
		// use empty config
	} else if instance.IsReplicationPrimaryOrStandalone() {
		request = PatroniConfig{
			StandbyCluster: nil,
		}
		if instance.Spec.PostgresConnection.SynchronousReplication {
			if synchronousStandbyApplicationName == nil {
				// fetch the sync standby to determine the correct application_name of the instance
				log.V(debugLogLevel).Info("unexpectedly having to fetch the referenced sync standby")
				s := &pg.Postgres{}
				ns := types.NamespacedName{
					Name:      instance.Spec.PostgresConnection.ConnectedPostgresID,
					Namespace: instance.Namespace,
				}
				if err := r.CtrlClient.Get(ctx, ns, s); err != nil {
					r.recorder.Eventf(s, "Warning", "Error", "failed to get referenced sync standby: %v", err)
					synchronousStandbyApplicationName = nil
				} else {
					synchronousStandbyApplicationName = ptr.To(s.ToPeripheralResourceName())
				}
			}
			// enable sync replication
			request.SynchronousNodesAdditional = synchronousStandbyApplicationName
		} else {
			// disable sync replication
			request.SynchronousNodesAdditional = nil
		}
	} else {
		request = PatroniConfig{
			StandbyCluster: &PatroniStandbyCluster{
				CreateReplicaMethods: []string{"basebackup_fast_xlog"},
				Host:                 instance.Spec.PostgresConnection.ConnectionIP,
				Port:                 int(instance.Spec.PostgresConnection.ConnectionPort),
				ApplicationName:      instance.ToPeripheralResourceName(),
			},
			SynchronousNodesAdditional: nil,
		}
	}
	log.V(debugLogLevel).Info("Prepared request", "request", request)
	jsonReq, err := json.Marshal(request)
	if err != nil {
		log.V(debugLogLevel).Info("could not create config")
		return err
	}

	httpClient := &http.Client{}
	url := "http://" + podIP + ":" + podPort + "/" + path

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewBuffer(jsonReq))
	if err != nil {
		log.Error(err, "could not create PATCH request")
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Error(err, "could not perform PATCH request")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		err = fmt.Errorf("received unexpected return code %d", resp.StatusCode)
		log.Error(err, "could not perform PATCH request")
		return err
	}

	log.V(debugLogLevel).Info("Performed request")

	// fake error when standbyApplicationName is required but not provided
	if instance.Spec.PostgresConnection != nil && instance.IsReplicationPrimaryOrStandalone() && instance.Spec.PostgresConnection.SynchronousReplication && synchronousStandbyApplicationName == nil {
		return fmt.Errorf("missing application_name of synchronous standby, disable synchronous replication")
	}

	// fake error when standbyApplicationName is required but not provided
	if instance.Spec.PostgresConnection != nil && instance.Spec.PostgresConnection.SynchronousReplication && synchronousStandbyApplicationName == nil {
		return fmt.Errorf("missing application_name of synchronous standby, disable synchronous replication")
	}

	return nil
}

func (r *PostgresReconciler) httpGetPatroniConfig(log logr.Logger, ctx context.Context, podIP string) (*PatroniConfig, error) {
	if podIP == "" {
		return nil, errors.New("podIP must not be empty")
	}

	podPort := "8008"
	path := "config"

	httpClient := &http.Client{}
	url := "http://" + podIP + ":" + podPort + "/" + path

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Error(err, "could not create GET request")
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Error(err, "could not perform GET request")
		return nil, err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Info("could not read body")
		return nil, err
	}
	var jsonResp PatroniConfig
	err = json.Unmarshal(body, &jsonResp)
	if err != nil {
		log.V(debugLogLevel).Info("could not parse config response")
		return nil, err
	}

	log.V(debugLogLevel).Info("Got config", "response", jsonResp)

	return &jsonResp, err
}

func (r *PostgresReconciler) getBackupConfig(ctx context.Context, ns, name string) (*pg.BackupConfig, error) {
	// fetch secret
	backupSecret := &corev1.Secret{}
	backupNamespace := types.NamespacedName{
		Name:      name,
		Namespace: ns,
	}
	if err := r.CtrlClient.Get(ctx, backupNamespace, backupSecret); err != nil {
		return nil, fmt.Errorf("error while getting the backup secret from control plane cluster: %w", err)
	}

	backupConfigJSON, ok := backupSecret.Data[pg.BackupConfigKey]
	if !ok {
		return nil, fmt.Errorf("no backupConfig stored in the secret")
	}
	var backupConfig pg.BackupConfig
	err := json.Unmarshal(backupConfigJSON, &backupConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal backupconfig:%w", err)
	}
	return &backupConfig, nil
}

func (r *PostgresReconciler) createOrUpdateNetPol(ctx context.Context, instance *pg.Postgres, etcdHost string) error {

	name := instance.ToPeripheralResourceName() + "-egress"
	namespace := instance.ToPeripheralResourceNamespace()

	pgPodMatchingLabels := instance.ToZalandoPostgresqlMatchingLabels()
	pgPodMatchingLabels[pg.ApplicationLabelName] = pg.ApplicationLabelValue

	spec := networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{
			MatchLabels: pgPodMatchingLabels,
		},
		Egress: []networkingv1.NetworkPolicyEgressRule{},
		PolicyTypes: []networkingv1.PolicyType{
			networkingv1.PolicyTypeEgress,
		},
	}

	// pgToPgEgress allows communication to the postgres pods
	pgToPgEgress := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: pgPodMatchingLabels,
				},
			},
		},
	}
	// add rule
	spec.Egress = append(spec.Egress, pgToPgEgress)

	// coreDNSEgress allows communication to the dns
	coreDNSEgress := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					// TODO make configurable
					MatchLabels: client.MatchingLabels{
						"gardener.cloud/purpose": "kube-system",
					},
				},
				PodSelector: &metav1.LabelSelector{
					// TODO make configurable
					MatchLabels: client.MatchingLabels{
						"k8s-app": "kube-dns",
					},
				},
			},
		},
	}
	// add rule
	spec.Egress = append(spec.Egress, coreDNSEgress)

	// etcd (if configured)
	if etcdHost != "" {
		var etcdPort intstr.IntOrString
		_, port, err := net.SplitHostPort(etcdHost)
		if err != nil {
			etcdPort = intstr.FromString(port)
		} else {
			etcdPort = intstr.FromInt(2379)
		}
		etcdProtocol := corev1.ProtocolTCP
		etcdEgress := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					IPBlock: &networkingv1.IPBlock{
						CIDR: "0.0.0.0/0",
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Port:     &etcdPort,
					Protocol: &etcdProtocol,
				},
			},
		}
		// add rule
		spec.Egress = append(spec.Egress, etcdEgress)
	}

	// allows communication to the S3 (and any other port 443...)
	s3Port := intstr.FromInt(443)
	s3Protocol := corev1.ProtocolTCP
	s3Egress := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: "0.0.0.0/0",
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Port:     &s3Port,
				Protocol: &s3Protocol,
			},
		},
	}
	// add rule
	spec.Egress = append(spec.Egress, s3Egress)

	// allows communication to the configured primary postgres
	if instance.Spec.PostgresConnection != nil && !instance.Spec.PostgresConnection.ReplicationPrimary {
		postgresPort := intstr.FromInt(int(instance.Spec.PostgresConnection.ConnectionPort))
		postgresProtocol := corev1.ProtocolTCP
		standbyToPrimaryEgress := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					IPBlock: &networkingv1.IPBlock{
						CIDR: instance.Spec.PostgresConnection.ConnectionIP + "/32", // TODO find a better solution
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Port:     &postgresPort,
					Protocol: &postgresProtocol,
				},
			},
		}
		// add rule
		spec.Egress = append(spec.Egress, standbyToPrimaryEgress)
	}

	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, np, func() error {
		np.Spec = spec
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy NetworkPolicy: %w", err)
	}

	return nil
}

// createOrUpdateExporterSidecarServices ensures the necessary services to access the sidecars exist
func (r *PostgresReconciler) createOrUpdateExporterSidecarServices(log logr.Logger, ctx context.Context, namespace string, c *corev1.ConfigMap, in *pg.Postgres) error {
	pesPort, error := strconv.ParseInt(c.Data[postgresExporterServicePortKeyName], 10, 32)
	if error != nil {
		log.Error(error, "postgres-exporter-service-port could not be parsed to int32, falling back to default value")
		pesPort = 9187
	}

	pesTargetPort, error := strconv.ParseInt(c.Data[postgresExporterServiceTargetPortKeyName], 10, 32)
	if error != nil {
		log.Error(error, "postgres-exporter-service-target-port could not be parsed to int32, falling back to default value")
		pesTargetPort = pesPort
	}

	labels := map[string]string{
		// pg.ApplicationLabelName: pg.ApplicationLabelValue, // TODO check if we still need that label, IsOperatorDeletable won't work anymore if we set it.
		"app": "postgres-exporter",
	}
	annotations := map[string]string{
		postgresExporterServiceTenantAnnotationName:    in.Spec.Tenant,
		postgresExporterServiceProjectIDAnnotationName: in.Spec.ProjectID,
	}

	pes := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        postgresExporterServiceName,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
	}

	pes.Spec.Ports = []corev1.ServicePort{
		{
			Name:       postgresExporterServicePortName,
			Port:       int32(pesPort), //nolint
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt(int(pesTargetPort)),
		},
	}
	selector := map[string]string{
		pg.ApplicationLabelName: pg.ApplicationLabelValue,
	}
	pes.Spec.Selector = selector
	pes.Spec.Type = corev1.ServiceTypeClusterIP

	// try to fetch any existing postgres-exporter service
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      postgresExporterServiceName,
	}
	old := &corev1.Service{}
	if err := r.SvcClient.Get(ctx, ns, old); err == nil {
		// service exists, overwriting it (but using the same clusterip)
		pes.Spec.ClusterIP = old.Spec.ClusterIP
		pes.ObjectMeta.ResourceVersion = old.GetObjectMeta().GetResourceVersion()
		if err := r.SvcClient.Update(ctx, pes); err != nil {
			return fmt.Errorf("error while updating the postgres-exporter service: %w", err)
		}
		log.V(debugLogLevel).Info("postgres-exporter service updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := r.SvcClient.Create(ctx, pes); err != nil {
		return fmt.Errorf("error while creating the postgres-exporter service: %w", err)
	}
	log.V(debugLogLevel).Info("postgres-exporter service created")

	return nil
}

// deleteNetPol Deletes our NetworkPolicy, if it exists. This is probably only necessary if ENABLE_NETPOL is flipped at runtime, as the the NetworkPolicy is created in the databases namespace, which will be completely removed when the database is deleted.
func (r *PostgresReconciler) deleteNetPol(ctx context.Context, instance *pg.Postgres) error {
	netpol := &networkingv1.NetworkPolicy{}
	netpol.Namespace = instance.ToPeripheralResourceNamespace()
	netpol.Name = instance.ToPeripheralResourceName() + "-egress"
	if err := r.SvcClient.Delete(ctx, netpol); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to delete NetworkPolicy %v: %w", netpol.Name, err)
	}
	return nil
}

// createOrUpdateExporterSidecarServiceMonitor ensures the servicemonitors for the sidecars exist
func (r *PostgresReconciler) createOrUpdateExporterSidecarServiceMonitor(log logr.Logger, ctx context.Context, namespace string, in *pg.Postgres) error {
	labels := map[string]string{
		"app":     "postgres-exporter",
		"release": "prometheus",
	}

	annotations := map[string]string{
		postgresExporterServiceTenantAnnotationName:    in.Spec.Tenant,
		postgresExporterServiceProjectIDAnnotationName: in.Spec.ProjectID,
	}

	// TODO what's the correct name?

	pesm := &coreosv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:        postgresExporterServiceName,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
	}

	pesm.Spec.Endpoints = []coreosv1.Endpoint{
		{
			Port: postgresExporterServicePortName,
		},
	}
	pesm.Spec.NamespaceSelector = coreosv1.NamespaceSelector{
		MatchNames: []string{namespace},
	}
	matchLabels := map[string]string{
		// TODO use extracted string
		"app": "postgres-exporter",
	}
	pesm.Spec.Selector = metav1.LabelSelector{
		MatchLabels: matchLabels,
	}
	pesm.Spec.TargetLabels = []string{
		"postgres_partition_id=" + in.Spec.PartitionID,
		"is_primary=" + strconv.FormatBool(in.IsReplicationPrimaryOrStandalone()),
	}

	// try to fetch any existing postgres-exporter service
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      postgresExporterServiceName,
	}
	old := &coreosv1.ServiceMonitor{}
	if err := r.SvcClient.Get(ctx, ns, old); err == nil {
		// Copy the resource version
		pesm.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
		if err := r.SvcClient.Update(ctx, pesm); err != nil {
			return fmt.Errorf("error while updating the postgres-exporter servicemonitor: %w", err)
		}
		log.V(debugLogLevel).Info("postgres-exporter servicemonitor updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := r.SvcClient.Create(ctx, pesm); err != nil {
		return fmt.Errorf("error while creating the postgres-exporter servicemonitor: %w", err)
	}
	log.V(debugLogLevel).Info("postgres-exporter servicemonitor created")

	return nil
}

// createOrUpdatePatroniPodMonitor ensures the servicemonitors for the sidecars exist
func (r *PostgresReconciler) createOrUpdatePatroniPodMonitor(ctx context.Context, namespace string, in *pg.Postgres) error {
	log := r.Log.WithValues("namespace", namespace)

	labels := map[string]string{
		"app":     "postgres-exporter",
		"release": "prometheus",
	}

	annotations := map[string]string{
		postgresExporterServiceTenantAnnotationName:    in.Spec.Tenant,
		postgresExporterServiceProjectIDAnnotationName: in.Spec.ProjectID,
	}

	pm := &coreosv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podMonitorName,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
	}

	pm.Spec.PodMetricsEndpoints = []coreosv1.PodMetricsEndpoint{
		{
			Port: ptr.To(podMonitorPort),
		},
	}
	pm.Spec.NamespaceSelector = coreosv1.NamespaceSelector{
		MatchNames: []string{namespace},
	}
	matchLabels := map[string]string{
		"application": "spilo",
	}
	pm.Spec.Selector = metav1.LabelSelector{
		MatchLabels: matchLabels,
	}

	// try to fetch any existing podmonitor
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      podMonitorName,
	}
	old := &coreosv1.PodMonitor{}
	if err := r.SvcClient.Get(ctx, ns, old); err == nil {
		// Copy the resource version
		pm.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
		if err := r.SvcClient.Update(ctx, pm); err != nil {
			return fmt.Errorf("error while updating the podmonitor: %w", err)
		}
		log.Info("pod monitor updated")
		return nil
	}

	// local podmonitor does not exist, creating it
	if err := r.SvcClient.Create(ctx, pm); err != nil {
		return fmt.Errorf("error while creating the podmonitor: %w", err)
	}
	log.Info("podmonitor created")

	return nil
}

func (r *PostgresReconciler) deleteExporterSidecarService(log logr.Logger, ctx context.Context, namespace string) error {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      postgresExporterServiceName,
			Namespace: namespace,
		},
	}
	if err := r.SvcClient.Delete(ctx, s); err != nil {
		return fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
	}
	log.V(debugLogLevel).Info("postgres-exporter service deleted")

	return nil
}

func (r *PostgresReconciler) getWalGEncryptionSecret(ctx context.Context) (*corev1.Secret, error) {

	ns := r.PostgresletNamespace
	name := r.PostgresletFullname + walGEncryptionSecretNamePostfix

	// fetch secret
	s := &corev1.Secret{}
	nn := types.NamespacedName{
		Name:      name,
		Namespace: ns,
	}
	if err := r.SvcClient.Get(ctx, nn, s); err != nil {
		return nil, fmt.Errorf("error while getting the backup secret from service cluster: %w", err)
	}

	return s, nil
}

func (r *PostgresReconciler) ensureStorageEncryptionSecret(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {

	if !r.EnableRandomStorageEncryptionSecret {
		log.V(debugLogLevel).Info("storage secret disabled, no action needed")
		return nil
	}

	// Check if secrets exist local in SERVICE Cluster
	n := storageEncryptionKeyName
	ns := instance.ToPeripheralResourceNamespace()
	s := &corev1.Secret{}
	log.V(debugLogLevel).Info("checking for storage secret", "name", n)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: n}, s)
	if err == nil {
		log.V(debugLogLevel).Info("storage secret found, no action needed")
		return nil
	}

	// we got an error other than not found, so we cannot continue!
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("error while fetching storage secret from service cluster: %w", err)
	}

	log.V(debugLogLevel).Info("creating storage secret")

	k, err := r.generateRandomString()
	if err != nil {
		return fmt.Errorf("error while generating random storage secret: %w", err)
	}

	postgresSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       n,
			Namespace:  ns,
			Finalizers: []string{storageEncryptionKeyFinalizerName},
		},
		StringData: map[string]string{
			"host-encryption-passphrase": k,
		},
	}

	if err := r.SvcClient.Create(ctx, postgresSecret); err != nil {
		return fmt.Errorf("error while creating storage secret in service cluster: %w", err)
	}
	log.V(debugLogLevel).Info("created storage secret", "secret", postgresSecret)

	return nil

}

func (r *PostgresReconciler) generateRandomString() (string, error) {
	const chars string = "!\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"
	var size *big.Int = big.NewInt(int64(len(chars)))
	b := make([]byte, 64)
	for i := range b {
		x, err := rand.Int(rand.Reader, size)
		if err != nil {
			return "", err
		}
		b[i] = chars[x.Int64()]
	}
	return string(b), nil
}

func (r *PostgresReconciler) removeStorageEncryptionSecretFinalizer(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {

	// Fetch secret
	n := storageEncryptionKeyName
	ns := instance.ToPeripheralResourceNamespace()
	s := &corev1.Secret{}
	log.V(debugLogLevel).Info("Fetching storage secret", "name", n)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: n}, s)
	if err != nil {
		// TODO this would be blocking if we couldn't remove the finalizer!
		return fmt.Errorf("error while fetching storage secret from service cluster: %w", err)
	}

	// Remove finalizer
	s.ObjectMeta.Finalizers = removeElem(s.ObjectMeta.Finalizers, storageEncryptionKeyFinalizerName)
	if err := r.SvcClient.Update(ctx, s); err != nil {
		return fmt.Errorf("error while removing finalizer from storage secret in service cluster: %w", err)
	}

	log.V(debugLogLevel).Info("finalizer removed from storage secret", "name", n)
	return nil
}

func removeElem(ss []string, s string) (out []string) {
	for _, elem := range ss {
		if elem == s {
			continue
		}
		out = append(out, elem)
	}
	return
}

func (r *PostgresReconciler) ensureInitDBJob(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {
	ns := types.NamespacedName{
		Namespace: instance.ToPeripheralResourceNamespace(),
		Name:      initDBName,
	}
	cm := &corev1.ConfigMap{}
	if err := r.SvcClient.Get(ctx, ns, cm); err == nil {
		// configmap already exists, nothing to do here
		log.V(debugLogLevel).Info("initdb ConfigMap already exists")
		return nil
	}

	// create initDB configmap
	cm.Name = ns.Name
	cm.Namespace = ns.Namespace
	cm.Data = map[string]string{}

	// only execute SQL when encountering a **new** database, not for standbies or clones
	if instance.IsReplicationPrimaryOrStandalone() && instance.Spec.PostgresRestore == nil {
		// try to fetch the global initjob configmap
		cns := types.NamespacedName{
			Namespace: r.PostgresletNamespace,
			Name:      r.InitDBJobConfigMapName,
		}
		globalInitjobCM := &corev1.ConfigMap{}
		if err := r.SvcClient.Get(ctx, cns, globalInitjobCM); err == nil {
			cm.Data = globalInitjobCM.Data
		} else {
			log.Error(err, "global initdb ConfigMap could not be loaded, using dummy data")
			// fall back to dummy data
			cm.Data["initdb.sql"] = initDBSQLDummy
		}
	} else {
		// use dummy job for standbies and clones
		cm.Data["initdb.sql"] = initDBSQLDummy
	}

	if err := r.SvcClient.Create(ctx, cm); err != nil {
		return fmt.Errorf("error while creating the new initdb ConfigMap: %w", err)
	}
	log.V(debugLogLevel).Info("new initdb ConfigMap created")

	if instance.IsReplicationTarget() || instance.Spec.PostgresRestore != nil {
		log.V(debugLogLevel).Info("initdb job not required")
		return nil
	}

	// create initDB job
	j := &batchv1.Job{}

	if err := r.SvcClient.Get(ctx, ns, j); err == nil {
		// job already exists, nothing to do here
		log.V(debugLogLevel).Info("initdb Job already exists")
		return nil // TODO return or update?
	}

	j.Name = ns.Name
	j.Namespace = ns.Namespace

	var uid int64 = 101
	var gid int64 = 101
	var ttl int32 = 180

	var backOffLimit int32 = 99
	j.Spec = batchv1.JobSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:    "psql",
						Image:   r.PostgresImage,
						Command: []string{"sh", "-c", "echo ${PGPASSWORD_SUPERUSER} | psql --host=${SCOPE} --port=5432 --username=${PGUSER_SUPERUSER} --file=/initdb.d/initdb.sql"},
						Env: []corev1.EnvVar{
							{
								Name:  "PGUSER_SUPERUSER",
								Value: "postgres",
							},
							{
								Name: "PGPASSWORD_SUPERUSER",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										Key: "password",
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "postgres." + instance.ToPeripheralResourceName() + ".credentials",
										},
									},
								},
							},
							{
								Name:  "SCOPE",
								Value: instance.ToPeripheralResourceName(),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Privileged:               ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							RunAsNonRoot:             ptr.To(true),
							RunAsUser:                ptr.To(uid),
							RunAsGroup:               ptr.To(gid),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
							SeccompProfile: &corev1.SeccompProfile{
								Type: corev1.SeccompProfileTypeRuntimeDefault,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      initDBName + "-volume",
								MountPath: "/initdb.d",
							},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
				Volumes: []corev1.Volume{
					{
						Name: initDBName + "-volume",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: initDBName,
								},
							},
						},
					},
				},
			},
		},
		BackoffLimit:            &backOffLimit,
		TTLSecondsAfterFinished: ptr.To(ttl),
	}

	if err := r.SvcClient.Create(ctx, j); err != nil {
		return fmt.Errorf("error while creating the new initdb Job: %w", err)
	}
	log.V(debugLogLevel).Info("new initdb Job created")

	return nil
}

func (r *PostgresReconciler) createOrUpdateCertificate(log logr.Logger, ctx context.Context, instance *pg.Postgres) error {
	if r.TLSClusterIssuer == "" {
		log.V(debugLogLevel).Info("certificate skipped")
		return nil
	}

	commonName := instance.ToPeripheralResourceName()
	if r.TLSSubDomain != "" {
		commonName = instance.ToDNSName(r.TLSSubDomain)
	}

	c := &cmapi.Certificate{ObjectMeta: metav1.ObjectMeta{Name: instance.ToPeripheralResourceName(), Namespace: instance.ToPeripheralResourceNamespace()}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, c, func() error {
		c.Spec = cmapi.CertificateSpec{
			CommonName: commonName,
			SecretName: instance.ToTLSSecretName(),
			IssuerRef: cmmeta.ObjectReference{
				Group: "cert-manager.io",
				Kind:  "ClusterIssuer",
				Name:  r.TLSClusterIssuer,
			},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unable to create or update certificate: %w", err)
	}

	log.V(debugLogLevel).Info("certificate created or updated")
	return nil
}

// createOrUpdateWalGExporterDeployment ensures the deployment for the wal-g-exporter
func (r *PostgresReconciler) createOrUpdateWalGExporterDeployment(log logr.Logger, ctx context.Context, namespace string, instance *pg.Postgres) error {
	labels := map[string]string{
		"app.kubernetes.io/name": walGExporterName,
		pg.UIDLabelName:          string(instance.UID),
		pg.NameLabelName:         instance.Name,
		pg.TenantLabelName:       instance.Spec.Tenant,
		pg.ProjectIDLabelName:    instance.Spec.ProjectID,
		pg.PartitionIDLabelName:  instance.Spec.PartitionID,
	}

	annotations := map[string]string{}

	matchLabels := labels

	var replicas int32 = 1
	var uid int64 = 65534
	var gid int64 = 65534

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        walGExporterName,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: matchLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        walGExporterName,
					Namespace:   namespace,
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Command: []string{"wal-g-prometheus-exporter"},
							Env: []corev1.EnvVar{
								{
									Name:  "PGHOST",
									Value: instance.ToPeripheralResourceName(),
								},
								{
									Name:  "PGPORT",
									Value: "5432",
								},
								{
									Name:  "PGDATABASE",
									Value: "postgres",
								},
								{
									Name:  "PGUSER",
									Value: "monitoring",
								},
								{
									Name: "PGPASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											Key: "password",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "monitoring." + instance.ToPeripheralResourceName() + ".credentials",
											},
										},
									},
								},
								{
									Name:  "SCOPE",
									Value: instance.ToPeripheralResourceName(),
								},
								{
									Name: "AWS_ACCESS_KEY_ID",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											Key: "AWS_ACCESS_KEY_ID",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: operatormanager.PodEnvSecretName,
											},
										},
									},
								},
								{
									Name: "AWS_SECRET_ACCESS_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											Key: "AWS_SECRET_ACCESS_KEY",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: operatormanager.PodEnvSecretName,
											},
										},
									},
								},
								{
									Name: "AWS_ENDPOINT",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											Key: "AWS_ENDPOINT",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: operatormanager.PodEnvCMName,
											},
										},
									},
								},
								{
									Name: "AWS_S3_FORCE_PATH_STYLE",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											Key: "AWS_S3_FORCE_PATH_STYLE",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: operatormanager.PodEnvCMName,
											},
										},
									},
								},
								{
									Name: "WALG_S3_PREFIX",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											Key: "WALG_S3_PREFIX",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: operatormanager.PodEnvCMName,
											},
										},
									},
								},
							},
							Image: r.WalGExporterImage,
							Name:  walGExporterName,
							Ports: []corev1.ContainerPort{
								{
									Name:          walGExporterName,
									ContainerPort: walGExporterPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(r.WalGExporterCPULimit),
									corev1.ResourceMemory: resource.MustParse(r.WalGExporterMemoryLimit),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Privileged:               ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								RunAsNonRoot:             ptr.To(true),
								RunAsUser:                ptr.To(uid),
								RunAsGroup:               ptr.To(gid),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tmp",
									MountPath: "/tmp",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "tmp",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	// try to fetch any existing deployment
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      walGExporterName,
	}
	old := &appsv1.Deployment{}
	if err := r.SvcClient.Get(ctx, ns, old); err == nil {
		// Copy the resource version
		deploy.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
		if err := r.SvcClient.Update(ctx, deploy); err != nil {
			return fmt.Errorf("error while updating the wal-g-exporter deployment: %w", err)
		}
		log.Info("wal-g-exporter deployment updated")
		return nil
	}

	// local deployment does not exist, creating it
	if err := r.SvcClient.Create(ctx, deploy); err != nil {
		return fmt.Errorf("error while creating the wal-g-exporter deployment: %w", err)
	}
	log.Info("wal-g-exporter deployment created")

	return nil
}

// deleteWalGExporterDeployment Deletes our wal-g-exporter Deployment, if it exists.
func (r *PostgresReconciler) deleteWalGExporterDeployment(ctx context.Context, namespace string) error {
	d := &appsv1.Deployment{}
	d.Namespace = namespace
	d.Name = walGExporterName
	if err := r.SvcClient.Delete(ctx, d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error while deleting the wal-g-exporter deployment: %w", err)
	}
	return nil
}

// createOrUpdateWalGExporterPodMonitor ensures the necessary services to access the wal-g-exporter exist
func (r *PostgresReconciler) createOrUpdateWalGExporterPodMonitor(log logr.Logger, ctx context.Context, namespace string, in *pg.Postgres) error {
	l := map[string]string{
		"app.kubernetes.io/name": walGExporterName,
		pg.UIDLabelName:          string(in.UID),
		pg.NameLabelName:         in.Name,
		pg.TenantLabelName:       in.Spec.Tenant,
		pg.ProjectIDLabelName:    in.Spec.ProjectID,
		pg.PartitionIDLabelName:  in.Spec.PartitionID,
	}
	a := map[string]string{
		postgresExporterServiceTenantAnnotationName:    in.Spec.Tenant,
		postgresExporterServiceProjectIDAnnotationName: in.Spec.ProjectID,
	}

	s := &coreosv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:        walGExporterName,
			Namespace:   namespace,
			Labels:      l,
			Annotations: a,
		},
	}

	s.Spec.PodMetricsEndpoints = []coreosv1.PodMetricsEndpoint{
		{
			Port: ptr.To(strconv.Itoa(int(walGExporterPort))),
		},
	}
	selector := map[string]string{
		"app.kubernetes.io/name": walGExporterName,
	}
	s.Spec.Selector = metav1.LabelSelector{MatchLabels: selector}

	// try to fetch any existing wal-g-exporter podMonitor
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      walGExporterName,
	}
	old := &coreosv1.PodMonitor{}
	if err := r.SvcClient.Get(ctx, ns, old); err == nil {
		// podMonitor exists, overwriting it
		s.ObjectMeta.ResourceVersion = old.GetObjectMeta().GetResourceVersion()
		if err := r.SvcClient.Update(ctx, s); err != nil {
			return fmt.Errorf("error while updating the wal-g-exporter podMonitor: %w", err)
		}
		log.V(debugLogLevel).Info("wal-g-exporter podMonitor updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := r.SvcClient.Create(ctx, s); err != nil {
		return fmt.Errorf("error while creating the wal-g-exporter podMonitor: %w", err)
	}
	log.V(debugLogLevel).Info("wal-g-exporter podMonitor created")

	return nil
}

// deleteWalGExporterPodMonitor Deletes our wal-g-exporter PodMonitor, if it exists.
func (r *PostgresReconciler) deleteWalGExporterPodMonitor(ctx context.Context, namespace string) error {
	d := &coreosv1.PodMonitor{}
	d.Namespace = namespace
	d.Name = walGExporterName
	if err := r.SvcClient.Delete(ctx, d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error while deleting the wal-g-exporter podMonitor: %w", err)
	}
	return nil
}
