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
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"

	firewall "github.com/metal-stack/firewall-controller/api/v1"
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
)

const (
	postgresExporterServiceName                    string = "postgres-exporter"
	postgresExporterServicePortName                string = "metrics"
	postgresExporterServiceTenantAnnotationName    string = pg.TenantLabelName
	postgresExporterServiceProjectIDAnnotationName string = pg.ProjectIDLabelName
	storageEncryptionKeyName                       string = "storage-encryption-key"
	storageEncryptionKeyFinalizerName              string = "postgres.database.fits.cloud/secret-finalizer"
	walGEncryptionSecretNamePostfix                string = "-walg-encryption"
	walGEncryptionSecretKeyName                    string = "key"
)

// requeue defines in how many seconds a requeue should happen
var requeue = ctrl.Result{
	Requeue: true,
}

// PostgresReconciler reconciles a Postgres object
type PostgresReconciler struct {
	CtrlClient                          client.Client
	SvcClient                           client.Client
	Log                                 logr.Logger
	Scheme                              *runtime.Scheme
	PartitionID, Tenant, StorageClass   string
	OperatorManager                     *operatormanager.OperatorManager
	LBManager                           *lbmanager.LBManager
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
	EnableRandomStorageEncryptionSecret bool
	EnableWalGEncryption                bool
	PostgresletFullname                 string
	EnableBootstrapStandbyFromS3        bool
}

// Reconcile is the entry point for postgres reconciliation.
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;list;watch
func (r *PostgresReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("postgres", req.NamespacedName)

	log.Info("reconciling")
	instance := &pg.Postgres{}
	if err := r.CtrlClient.Get(ctx, req.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			// the instance was updated, but does not exist anymore -> do nothing, it was probably deleted
			return ctrl.Result{}, nil
		}

		r.recorder.Eventf(instance, "Warning", "Error", "failed to get resource: %v", err)
		return ctrl.Result{}, err
	}
	log.Info("postgres fetched", "postgres", instance)

	if !r.isManagedByUs(instance) {
		log.Info("object should be managed by another postgreslet, ignored.")
		return ctrl.Result{}, nil
	}

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

		if err := r.deleteCWNP(ctx, instance); client.IgnoreNotFound(err) != nil { // todo: remove ignorenotfound
			r.recorder.Event(instance, "Warning", "Error", "failed to delete ClusterwideNetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("corresponding CRD ClusterwideNetworkPolicy deleted")

		if err := r.LBManager.DeleteSharedSvcLB(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Service with shared ip: %v", err)
			return ctrl.Result{}, err
		}

		if err := r.LBManager.DeleteDedicatedSvcLB(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Service with dedicated ip: %v", err)
			return ctrl.Result{}, err
		}
		log.Info("corresponding Service(s) of type LoadBalancer deleted")

		// delete the postgres-exporter service
		if err := r.deleteExporterSidecarService(ctx, namespace); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
		}

		if err := r.deleteZPostgresqlByLabels(ctx, matchingLabels, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Zalando resource: %v", err)
			return ctrl.Result{}, err
		}
		log.Info("owned zalando postgresql deleted")

		if err := r.deleteNetPol(ctx, instance); err != nil {
			log.Error(err, "failed to delete NetworkPolicy")
		} else {
			log.Info("corresponding NetworkPolicy deleted")
		}

		if err := r.removeStorageEncryptionSecretFinalizer(ctx, instance); err != nil {
			log.Error(err, "error while remnoving finalizer from storage encryption secret")
		} else {

			log.Info("finalizer from storage encryption secret removed")
		}

		deletable, err := r.OperatorManager.IsOperatorDeletable(ctx, namespace)
		if err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to check if the operator is idle: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while checking if the operator is idle: %w", err)
		}
		if !deletable {
			r.recorder.Event(instance, "Warning", "Self-Reconcilation", "operator not yet deletable, requeueing")
			log.Info("operator not yet deletable, requeueing")
			return ctrl.Result{Requeue: true}, nil
		}
		if err := r.OperatorManager.UninstallOperator(ctx, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to uninstall operator: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while uninstalling operator: %w", err)
		}
		log.Info("corresponding operator deleted")

		if err := r.deleteUserPasswordsSecret(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("corresponding passwords secret deleted")

		instance.RemoveFinalizer(pg.PostgresFinalizerName)
		if err := r.CtrlClient.Update(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Self-Reconcilation", "failed to remove finalizer: %v", err)
			return ctrl.Result{}, fmt.Errorf("failed to update finalizers: %w", err)
		}
		log.Info("finalizers removed")

		return ctrl.Result{}, nil
	}

	// Add finalizer if none.
	if !instance.HasFinalizer(pg.PostgresFinalizerName) {
		instance.AddFinalizer(pg.PostgresFinalizerName)
		if err := r.CtrlClient.Update(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Self-Reconcilation", "failed to add finalizer: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while adding finalizer: %w", err)
		}
		log.Info("finalizer added")
	}

	// Check if zalando dependencies are installed. If not, install them.
	if err := r.ensureZalandoDependencies(ctx, instance); err != nil {
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

	// Make sure the postgres secrets exist, if neccessary
	if err := r.ensurePostgresSecrets(ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create postgres secrets: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating postgres secrets: %w", err)
	}

	if instance.IsReplicationPrimary() {
		// the field is not empty, which means we were either a regular, standalone database that was promoted to leader
		//  (meaning we are already running) or we are a standby which was promoted to leader (also meaning we are
		// already running)
		// That means we should be able to call the patroni api already. this is required, as updating the custom
		// ressource of a standby db seems to fail (maybe because of the users/databases?)...
		// anyway, let's get on with it
		if err := r.updatePatroniConfig(ctx, instance); err != nil {
			// TODO what to do here? reschedule or ignore?
			log.Error(err, "failed to update patroni config via REST call")
		}
	}

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
	if err := r.createOrUpdateExporterSidecarServices(ctx, namespace, globalSidecarsCM, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating sidecars services %v: %w", namespace, err)
	}

	// Add service monitor for our exporter sidecar
	err := r.createOrUpdateExporterSidecarServiceMonitor(ctx, namespace, instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating sidecars servicemonitor %v: %w", namespace, err)
	}

	// Make sure the storage secret exist, if neccessary
	if err := r.ensureStorageEncryptionSecret(ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create storage secret: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating storage secret: %w", err)
	}

	if err := r.createOrUpdateZalandoPostgresql(ctx, instance, log, globalSidecarsCM, r.PatroniTTL, r.PatroniLoopWait, r.PatroniRetryTimeout); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create Zalando resource: %v", err)
		return ctrl.Result{}, fmt.Errorf("failed to create or update zalando postgresql: %w", err)
	}

	if err := r.LBManager.ReconcileSvcLBs(ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create Service: %v", err)
		return ctrl.Result{}, err
	}

	// Check if socket port is ready
	port := instance.Status.Socket.Port
	if port == 0 {
		r.recorder.Event(instance, "Warning", "Self-Reconcilation", "socket port not ready")
		log.Info("socket port not ready")
		return requeue, nil
	}

	// Update status will be handled by the StatusReconciler, based on the Zalando Status
	if err := r.createOrUpdateIngressCWNP(ctx, instance, int(port)); err != nil {
		r.recorder.Event(instance, "Warning", "Error", "failed to create or update ingress ClusterwideNetworkPolicy")
		return ctrl.Result{}, fmt.Errorf("unable to create or update ingress ClusterwideNetworkPolicy: %w", err)
	}

	// this is the call for standbys
	if err := r.updatePatroniConfig(ctx, instance); err != nil {
		return requeue, fmt.Errorf("unable to update patroni config: %w", err)
	}

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

func (r *PostgresReconciler) createOrUpdateZalandoPostgresql(ctx context.Context, instance *pg.Postgres, log logr.Logger, sidecarsCM *corev1.ConfigMap, patroniTTL, patroniLoopWait, patroniRetryTimout uint32) error {
	var restoreBackupConfig *pg.BackupConfig
	var restoreSouceInstance *pg.Postgres
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
			log.Info("source for restore fetched", "postgres", instance)

			bc, err := r.getBackupConfig(ctx, instance.Namespace, src.Spec.BackupSecretRef)
			if err != nil {
				return err
			}

			restoreBackupConfig = bc
			restoreSouceInstance = src
		}
	}

	// Get zalando postgresql and create one if none.
	rawZ, err := r.getZalandoPostgresql(ctx, instance)
	if err != nil {
		// errors other than `NotFound`
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to fetch zalando postgresql: %w", err)
		}

		u, err := instance.ToUnstructuredZalandoPostgresql(nil, sidecarsCM, r.StorageClass, r.PgParamBlockList, restoreBackupConfig, restoreSouceInstance, patroniTTL, patroniLoopWait, patroniRetryTimout)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
		}

		if err := r.SvcClient.Create(ctx, u); err != nil {
			return fmt.Errorf("failed to create zalando postgresql: %w", err)
		}
		log.Info("zalando postgresql created", "zalando postgresql", u)

		return nil
	}

	// Update zalando postgresql
	mergeFrom := client.MergeFrom(rawZ.DeepCopy())

	u, err := instance.ToUnstructuredZalandoPostgresql(rawZ, sidecarsCM, r.StorageClass, r.PgParamBlockList, restoreBackupConfig, restoreSouceInstance, patroniTTL, patroniLoopWait, patroniRetryTimout)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
	}
	if err := r.SvcClient.Patch(ctx, u, mergeFrom); err != nil {
		return fmt.Errorf("failed to update zalando postgresql: %w", err)
	}
	log.Info("zalando postgresql updated", "zalando postgresql", u)

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
func (r *PostgresReconciler) ensureZalandoDependencies(ctx context.Context, p *pg.Postgres) error {
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

	if err := r.updatePodEnvironmentConfigMap(ctx, p); err != nil {
		return fmt.Errorf("error while updating backup config: %w", err)
	}

	if err := r.updatePodEnvironmentSecret(ctx, p); err != nil {
		return fmt.Errorf("error while updating backup config secret: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) updatePodEnvironmentConfigMap(ctx context.Context, p *pg.Postgres) error {
	log := r.Log.WithValues("postgres", p.Name)
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
		log.Info("mising Pod Environment ConfigMap created!")
	}
	cm.Data = data
	if err := r.SvcClient.Update(ctx, cm); err != nil {
		return fmt.Errorf("error while updating the pod environment configmap in service cluster: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) updatePodEnvironmentSecret(ctx context.Context, p *pg.Postgres) error {
	log := r.Log.WithValues("postgres", p.Name)
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
	ns := types.NamespacedName{
		Name:      operatormanager.PodEnvCMName,
		Namespace: p.ToPeripheralResourceNamespace(),
	}

	if s, err = r.OperatorManager.CreateOrGetPodEnvironmentSecret(ctx, ns.Namespace); err != nil {
		return fmt.Errorf("error while accessing the pod environment secret %v: %w", ns.Namespace, err)
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

func (r *PostgresReconciler) deleteZPostgresqlByLabels(ctx context.Context, matchingLabels client.MatchingLabels, namespace string) error {

	items, err := r.getZPostgresqlByLabels(ctx, matchingLabels, namespace)
	if err != nil {
		return err
	}

	for i, rawZ := range items {
		log := r.Log.WithValues("zalando postgresql", rawZ)
		if err := r.SvcClient.Delete(ctx, &items[i]); err != nil {
			return fmt.Errorf("error while deleting zalando postgresql: %w", err)
		}
		log.Info("zalando postgresql deleted")
	}

	return nil
}

// todo: Change to `controllerutl.CreateOrPatch`
// createOrUpdateIngressCWNP will create an ingress firewall rule on the firewall in front of the k8s cluster
// based on the spec.AccessList.SourceRanges and pre-configured standby clusters source ranges sgiven.
func (r *PostgresReconciler) createOrUpdateIngressCWNP(ctx context.Context, in *pg.Postgres, port int) error {
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
	r.Log.WithValues("postgres", in.ToKey()).Info("clusterwidenetworkpolicy created or updated")

	if in.Spec.PostgresConnection == nil {
		// abort if there are no connected postgres instances
		return nil
	}

	// Create CWNP if standby is configured (independant of the current role)

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

func (r *PostgresReconciler) deleteCWNP(ctx context.Context, in *pg.Postgres) error {
	stdbyIngresPolicy := &firewall.ClusterwideNetworkPolicy{}
	stdbyIngresPolicy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	stdbyIngresPolicy.Name = in.ToStandbyClusterIngresCWNPName()
	if err := r.SvcClient.Delete(ctx, stdbyIngresPolicy); err != nil {
		r.Log.Info("could not delete standby cluster ingress policy")
	}

	stdbyEgresPolicy := &firewall.ClusterwideNetworkPolicy{}
	stdbyEgresPolicy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	stdbyEgresPolicy.Name = in.ToStandbyClusterEgresCWNPName()
	if err := r.SvcClient.Delete(ctx, stdbyEgresPolicy); err != nil {
		r.Log.Info("could not delete standby cluster egress policy")
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

func (r *PostgresReconciler) ensurePostgresSecrets(ctx context.Context, instance *pg.Postgres) error {

	if err := r.ensureStandbySecrets(ctx, instance); err != nil {
		return err
	}

	if err := r.ensureCloneSecrets(ctx, instance); err != nil {
		return err
	}

	return nil

}

func (r *PostgresReconciler) ensureStandbySecrets(ctx context.Context, instance *pg.Postgres) error {
	if instance.IsReplicationPrimary() {
		// nothing is configured, or we are the leader. nothing to do.
		return nil
	}

	//  Check if instance.Spec.PostgresConnectionInfo.ConnectionSecretName is defined
	if instance.Spec.PostgresConnection.ConnectionSecretName == "" {
		return errors.New("connectionInfo.secretName not configured")
	}

	// Check if secrets exist local in SERVICE Cluster
	localStandbySecretName := operatormanager.PostgresConfigReplicationUsername + "." + instance.ToPeripheralResourceName() + ".credentials"
	localSecretNamespace := instance.ToPeripheralResourceNamespace()
	localStandbySecret := &corev1.Secret{}
	r.Log.Info("checking for local standby secret", "namespace", localSecretNamespace, "name", localStandbySecretName)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: localSecretNamespace, Name: localStandbySecretName}, localStandbySecret)

	if err == nil {
		r.Log.Info("local standby secret found, no action needed")
		return nil
	}

	// we got an error other than not found, so we cannot continue!
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("error while fetching local stadnby secret from service cluster: %w", err)
	}

	r.Log.Info("no local standby secret found, continuing to create one")

	remoteSecretNamespacedName := types.NamespacedName{
		Namespace: instance.ObjectMeta.Namespace,
		Name:      instance.Spec.PostgresConnection.ConnectionSecretName,
	}
	return r.copySecrets(ctx, remoteSecretNamespacedName, instance, false)

}

func (r *PostgresReconciler) ensureCloneSecrets(ctx context.Context, instance *pg.Postgres) error {
	if instance.Spec.PostgresRestore == nil {
		// not a clone. nothing to do.
		return nil
	}

	//  Check if instance.Spec.PostgresConnectionInfo.ConnectionSecretName is defined
	if instance.Spec.PostgresRestore.SourcePostgresID == "" {
		return errors.New("SourcePostgresID not configured")
	}

	// Check if secrets exist local in SERVICE Cluster
	localStandbySecretName := operatormanager.PostresConfigSuperUsername + "." + instance.ToPeripheralResourceName() + ".credentials"
	localSecretNamespace := instance.ToPeripheralResourceNamespace()
	localStandbySecret := &corev1.Secret{}
	r.Log.Info("checking for local postgres secret", "namespace", localSecretNamespace, "name", localStandbySecretName)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: localSecretNamespace, Name: localStandbySecretName}, localStandbySecret)

	if err == nil {
		r.Log.Info("local postgres secret found, no action needed")
		return nil
	}

	// we got an error other than not found, so we cannot continue!
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("error while fetching local stadnby secret from service cluster: %w", err)
	}

	r.Log.Info("no local postgres secret found, continuing to create one")

	remoteSecretName := strings.Replace(instance.ToUserPasswordsSecretName(), instance.Name, instance.Spec.PostgresRestore.SourcePostgresID, 1) // TODO this is hacky-wacky...
	remoteSecretNamespacedName := types.NamespacedName{
		Namespace: instance.ObjectMeta.Namespace,
		Name:      remoteSecretName,
	}
	return r.copySecrets(ctx, remoteSecretNamespacedName, instance, true)

}

func (r *PostgresReconciler) copySecrets(ctx context.Context, sourceSecret types.NamespacedName, targetInstance *pg.Postgres, ignoreStandbyUser bool) error {
	// Check if secrets exist in remote CONTROL Cluster
	remoteSecret := &corev1.Secret{}
	r.Log.Info("fetching remote postgres secret", "namespace", sourceSecret.Namespace, "name", sourceSecret.Name)
	if err := r.CtrlClient.Get(ctx, sourceSecret, remoteSecret); err != nil {
		// we cannot read the secret given in the configuration, so we cannot continue!
		return fmt.Errorf("error while fetching remote postgres secret from control plane: %w", err)
	}

	// copy all but the standby secrets...
	for username := range remoteSecret.Data {
		// check if we skip the standby user (e.g. to prevent old standby intances from connecting once a clone took over its sources ip/port)
		if ignoreStandbyUser && username == operatormanager.PostgresConfigReplicationUsername {
			continue
		}

		r.Log.Info("creating local secret", "username", username)

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
			return fmt.Errorf("error while creating local secrets in service cluster: %w", err)
		}
	}

	return nil
}

func (r *PostgresReconciler) updatePatroniConfig(ctx context.Context, instance *pg.Postgres) error {
	// Finally, send a POST to to the database with the correct config
	if instance.Spec.PostgresConnection == nil {
		return nil
	}

	r.Log.Info("Sending REST call to Patroni API")
	pods := &corev1.PodList{}

	roleReq, err := labels.NewRequirement(pg.SpiloRoleLabelName, selection.In, []string{pg.SpiloRoleLabelValueMaster, pg.SpiloRoleLabelValueStandbyLeader})
	if err != nil {
		r.Log.Info("could not create requirements for label selector to query pods, requeuing")
		return err
	}
	leaderSelector := labels.NewSelector()
	leaderSelector = leaderSelector.Add(*roleReq)

	opts := []client.ListOption{
		client.InNamespace(instance.ToPeripheralResourceNamespace()),
		client.MatchingLabelsSelector{Selector: leaderSelector},
	}
	if err := r.SvcClient.List(ctx, pods, opts...); err != nil {
		r.Log.Info("could not query pods, requeuing")
		return err
	}
	if len(pods.Items) == 0 {
		r.Log.Info("no leader pod found, selecting all spilo pods as a last resort (might be ok if it is still creating)")

		err = r.updatePatroniConfigOnAllPods(ctx, instance)
		if err != nil {
			r.Log.Info("updating patroni config failed, got one or more errors")
			return err
		}
		return nil
	}
	podIP := pods.Items[0].Status.PodIP

	return r.httpPatchPatroni(ctx, instance, podIP)
}

func (r *PostgresReconciler) updatePatroniConfigOnAllPods(ctx context.Context, instance *pg.Postgres) error {
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(instance.ToPeripheralResourceNamespace()),
		client.MatchingLabels{pg.ApplicationLabelName: pg.ApplicationLabelValue},
	}
	if err := r.SvcClient.List(ctx, pods, opts...); err != nil {
		r.Log.Info("could not query pods, requeuing")
		return err
	}

	if len(pods.Items) == 0 {
		r.Log.Info("no spilo pods found at all, requeueing")
		return errors.New("no spilo pods found at all")
	}

	// iterate all spilo pods
	var lastErr error
	for _, pod := range pods.Items {
		pod := pod // pin!
		podIP := pod.Status.PodIP
		if err := r.httpPatchPatroni(ctx, instance, podIP); err != nil {
			lastErr = err
			r.Log.Info("failed to update pod")
		}
	}
	if lastErr != nil {
		r.Log.Info("updating patroni config failed, got one or more errors")
		return lastErr
	}
	r.Log.Info("updating patroni config succeeded")
	return nil
}

func (r *PostgresReconciler) httpPatchPatroni(ctx context.Context, instance *pg.Postgres, podIP string) error {
	if podIP == "" {
		return errors.New("podIP must not be empty")
	}

	podPort := "8008"
	path := "config"

	type PatroniStandbyCluster struct {
		CreateReplicaMethods []string `json:"create_replica_methods"`
		Host                 string   `json:"host"`
		Port                 int      `json:"port"`
		ApplicationName      string   `json:"application_name"`
	}
	type PatroniConfigRequest struct {
		StandbyCluster             *PatroniStandbyCluster `json:"standby_cluster"`
		SynchronousNodesAdditional *string                `json:"synchronous_nodes_additional"`
	}

	r.Log.Info("Preparing request")
	var request PatroniConfigRequest
	if instance.IsReplicationPrimary() {
		request = PatroniConfigRequest{
			StandbyCluster: nil,
		}
		if instance.Spec.PostgresConnection.SynchronousReplication {
			// enable sync replication
			request.SynchronousNodesAdditional = pointer.String(instance.Spec.PostgresConnection.ConnectedPostgresID)
		} else {
			// disable sync replication
			request.SynchronousNodesAdditional = nil
		}
	} else {
		// TODO check values first
		request = PatroniConfigRequest{
			StandbyCluster: &PatroniStandbyCluster{
				CreateReplicaMethods: []string{"basebackup_fast_xlog"},
				Host:                 instance.Spec.PostgresConnection.ConnectionIP,
				Port:                 int(instance.Spec.PostgresConnection.ConnectionPort),
				ApplicationName:      instance.ObjectMeta.Name,
			},
			SynchronousNodesAdditional: nil,
		}
	}
	r.Log.Info("Prepared request", "request", request)
	jsonReq, err := json.Marshal(request)
	if err != nil {
		r.Log.Info("could not create config")
		return err
	}

	httpClient := &http.Client{}
	url := "http://" + podIP + ":" + podPort + "/" + path

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewBuffer(jsonReq))
	if err != nil {
		r.Log.Error(err, "could not create request")
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		r.Log.Error(err, "could not perform request")
		return err
	}
	defer resp.Body.Close()

	return nil
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

// createOrUpdateExporterSidecarServices ensures the neccessary services to acces the sidecars exist
func (r *PostgresReconciler) createOrUpdateExporterSidecarServices(ctx context.Context, namespace string, c *corev1.ConfigMap, in *pg.Postgres) error {
	log := r.Log.WithValues("namespace", namespace)

	exporterServicePort, error := strconv.ParseInt(c.Data["postgres-exporter-service-port"], 10, 32)
	if error != nil {
		// todo log error
		exporterServicePort = 9187
	}

	exporterServiceTargetPort, error := strconv.ParseInt(c.Data["postgres-exporter-service-target-port"], 10, 32)
	if error != nil {
		// todo log error
		exporterServiceTargetPort = exporterServicePort
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
			Port:       int32(exporterServicePort),
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt(int(exporterServiceTargetPort)),
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
		log.Info("postgres-exporter service updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := r.SvcClient.Create(ctx, pes); err != nil {
		return fmt.Errorf("error while creating the postgres-exporter service: %w", err)
	}
	log.Info("postgres-exporter service created")

	return nil
}

// deleteNetPol Deletes our NetworkPolicy, if it exists. This is probably only neccessary if ENABLE_NETPOL is flipped at runtime, as the the NetworkPolicy is created in the databases namespace, which will be completely removed when the database is deleted.
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
func (r *PostgresReconciler) createOrUpdateExporterSidecarServiceMonitor(ctx context.Context, namespace string, in *pg.Postgres) error {
	log := r.Log.WithValues("namespace", namespace)

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
		// TODO use extraced string
		"app": "postgres-exporter",
	}
	pesm.Spec.Selector = metav1.LabelSelector{
		MatchLabels: matchLabels,
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
		log.Info("postgres-exporter servicemonitor updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := r.SvcClient.Create(ctx, pesm); err != nil {
		return fmt.Errorf("error while creating the postgres-exporter servicemonitor: %w", err)
	}
	log.Info("postgres-exporter servicemonitor created")

	return nil
}

func (r *PostgresReconciler) deleteExporterSidecarService(ctx context.Context, namespace string) error {
	log := r.Log.WithValues("namespace", namespace)

	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      postgresExporterServiceName,
			Namespace: namespace,
		},
	}
	if err := r.SvcClient.Delete(ctx, s); err != nil {
		return fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
	}
	log.Info("postgres-exporter service deleted")

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

func (r *PostgresReconciler) ensureStorageEncryptionSecret(ctx context.Context, instance *pg.Postgres) error {

	if !r.EnableRandomStorageEncryptionSecret {
		r.Log.Info("storage secret disabled, no action needed")
		return nil
	}

	// Check if secrets exist local in SERVICE Cluster
	n := storageEncryptionKeyName
	ns := instance.ToPeripheralResourceNamespace()
	s := &corev1.Secret{}
	r.Log.Info("checking for storage secret", "namespace", ns, "name", n)
	err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: n}, s)
	if err == nil {
		r.Log.Info("storage secret found, no action needed")
		return nil
	}

	// we got an error other than not found, so we cannot continue!
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("error while fetching storage secret from service cluster: %w", err)
	}

	r.Log.Info("creating storage secret")

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
	r.Log.Info("created storage secret", "secret", postgresSecret)

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

func (r *PostgresReconciler) removeStorageEncryptionSecretFinalizer(ctx context.Context, instance *pg.Postgres) error {

	// Fetch secret
	n := storageEncryptionKeyName
	ns := instance.ToPeripheralResourceNamespace()
	s := &corev1.Secret{}
	r.Log.Info("Fetching storage secret", "namespace", ns, "name", n)
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
