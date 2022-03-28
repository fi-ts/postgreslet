/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
)

const (
	postgresExporterServiceName                    string = "postgres-exporter"
	postgresExporterServicePortName                string = "metrics"
	postgresExporterServiceTenantAnnotationName    string = pg.TenantLabelName
	postgresExporterServiceProjectIDAnnotationName string = pg.ProjectIDLabelName
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
	recorder                    record.EventRecorder
	PgParamBlockList            map[string]bool
	StandbyClustersSourceRanges []string
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

		if err := r.DeleteSvcLB(ctx, instance); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Service: %v", err)
			return ctrl.Result{}, err
		}
		log.Info("corresponding Service of type LoadBalancer deleted")

		// delete the postgres-exporter service
		if err := r.deleteExporterSidecarService(ctx, namespace); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
		}

		if err := r.deleteZPostgresqlByLabels(ctx, matchingLabels, namespace); err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to delete Zalando resource: %v", err)
			return ctrl.Result{}, err
		}
		log.Info("owned zalando postgresql deleted")

		deletable, err := r.IsOperatorDeletable(ctx, namespace)
		if err != nil {
			r.recorder.Eventf(instance, "Warning", "Error", "failed to check if the operator is idle: %v", err)
			return ctrl.Result{}, fmt.Errorf("error while checking if the operator is idle: %w", err)
		}
		if !deletable {
			r.recorder.Event(instance, "Warning", "Self-Reconcilation", "operator not yet deletable, requeueing")
			log.Info("operator not yet deletable, requeueing")
			return ctrl.Result{Requeue: true}, nil
		}
		if err := r.UninstallOperator(ctx, namespace); err != nil {
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

	// Make sure the standby secrets exist, if neccessary
	if err := r.ensureStandbySecrets(ctx, instance); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create standby secrets: %v", err)
		return ctrl.Result{}, fmt.Errorf("error while creating standby secrets: %w", err)
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
		// TODO don't use string literals here! name is dependent of the release name of the helm chart!
		Namespace: "postgreslet-system",
		Name:      "postgreslet-postgres-sidecars",
	}
	sidecarCM := &corev1.ConfigMap{}
	if err := r.SvcClient.Get(ctx, cns, sidecarCM); err != nil {
		// configmap with configuration does not exists, nothing we can do here...
		return ctrl.Result{}, fmt.Errorf("could not fetch config for sidecars")
	}
	// Add services for our sidecars
	namespace := instance.ToPeripheralResourceNamespace()
	if err := r.createOrUpdateExporterSidecarServices(ctx, namespace, sidecarCM, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating sidecars services %v: %w", namespace, err)
	}

	// Add service monitor for our exporter sidecar
	err := r.createOrUpdateExporterSidecarServiceMonitor(ctx, namespace, instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating sidecars servicemonitor %v: %w", namespace, err)
	}

	if err := r.createOrUpdateZalandoPostgresql(ctx, instance, log); err != nil {
		r.recorder.Eventf(instance, "Warning", "Error", "failed to create Zalando resource: %v", err)
		return ctrl.Result{}, fmt.Errorf("failed to create or update zalando postgresql: %w", err)
	}

	if err := r.CreateSvcLBIfNone(ctx, instance); err != nil {
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
		Complete(r)
}

func (r *PostgresReconciler) createOrUpdateZalandoPostgresql(ctx context.Context, instance *pg.Postgres, log logr.Logger) error {
	// Get the sidecar config
	// try to fetch the global sidecars configmap
	cns := types.NamespacedName{
		// TODO don't use string literals here! name is dependent of the release name of the helm chart!
		Namespace: "postgreslet-system",
		Name:      "postgreslet-postgres-sidecars",
	}
	c := &corev1.ConfigMap{}
	if err := r.SvcClient.Get(ctx, cns, c); err != nil {
		// configmap with configuration does not exists, nothing we can do here...
		log.Info("could not fetch config for sidecars")
		c = nil
	}

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
			r.recorder.Eventf(instance, "Warning", "Error", "failed to get resource: %v", err)
			return err
		}
		log.Info("source for restore fetched", "postgres", instance)

		bc, err := r.getBackupConfig(ctx, instance.Namespace, src.Spec.BackupSecretRef)
		if err != nil {
			return err
		}

		restoreBackupConfig = bc
		restoreSouceInstance = src
	}

	// Get zalando postgresql and create one if none.
	rawZ, err := r.getZalandoPostgresql(ctx, instance)
	if err != nil {
		// errors other than `NotFound`
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to fetch zalando postgresql: %w", err)
		}

		u, err := instance.ToUnstructuredZalandoPostgresql(nil, c, r.StorageClass, r.PgParamBlockList, restoreBackupConfig, restoreSouceInstance)
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

	u, err := instance.ToUnstructuredZalandoPostgresql(rawZ, c, r.StorageClass, r.PgParamBlockList, restoreBackupConfig, restoreSouceInstance)
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
	isInstalled, err := r.IsOperatorInstalled(ctx, namespace)
	if err != nil {
		return fmt.Errorf("error while querying if zalando dependencies are installed: %w", err)
	}

	if !isInstalled {
		if err := r.InstallOrUpdateOperator(ctx, namespace); err != nil {
			return fmt.Errorf("error while installing zalando dependencies: %w", err)
		}
	}

	if err := r.updatePodEnvironmentConfigMap(ctx, p); err != nil {
		return fmt.Errorf("error while updating backup config: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) updatePodEnvironmentConfigMap(ctx context.Context, p *pg.Postgres) error {
	log := r.Log.WithValues("postgres", p.UID)
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

	// use the rest as provided in the secret
	bucketName := backupConfig.S3BucketName
	awsAccessKeyID := backupConfig.S3AccessKey
	awsSecretAccessKey := backupConfig.S3SecretKey
	backupSchedule := backupConfig.Schedule
	backupNumToRetain := backupConfig.Retention

	// s3 server side encryption SSE is enabled if the key is given
	// TODO our s3 needs a small change to make this work
	walgDisableSSE := "true"
	walgSSE := ""
	if backupConfig.S3EncryptionKey != nil {
		walgDisableSSE = "false"
		walgSSE = *backupConfig.S3EncryptionKey
	}

	// create updated content for pod environment configmap
	data := map[string]string{
		"USE_WALG_BACKUP":                  "true",
		"USE_WALG_RESTORE":                 "true",
		"WALE_S3_PREFIX":                   "s3://" + bucketName + "/$(SCOPE)",
		"WALG_S3_PREFIX":                   "s3://" + bucketName + "/$(SCOPE)",
		"CLONE_WALG_S3_PREFIX":             "s3://" + bucketName + "/$(CLONE_SCOPE)",
		"WALE_BACKUP_THRESHOLD_PERCENTAGE": "100",
		"AWS_ENDPOINT":                     awsEndpoint,
		"WALE_S3_ENDPOINT":                 walES3Endpoint, // same as above, but slightly modified
		"AWS_ACCESS_KEY_ID":                awsAccessKeyID,
		"AWS_SECRET_ACCESS_KEY":            awsSecretAccessKey,
		"AWS_S3_FORCE_PATH_STYLE":          "true",
		"AWS_REGION":                       region,         // now we can use AWS S3
		"WALG_DISABLE_S3_SSE":              walgDisableSSE, // disable server side encryption if key is nil
		"WALG_S3_SSE":                      walgSSE,        // server side encryption key
		"BACKUP_SCHEDULE":                  backupSchedule,
		"BACKUP_NUM_TO_RETAIN":             backupNumToRetain,
	}

	cm := &corev1.ConfigMap{}
	ns := types.NamespacedName{
		Name:      operatormanager.PodEnvCMName,
		Namespace: p.ToPeripheralResourceNamespace(),
	}
	if err := r.SvcClient.Get(ctx, ns, cm); err != nil {
		return fmt.Errorf("error while getting the pod environment configmap from service cluster: %w", err)
	}
	cm.Data = data
	if err := r.SvcClient.Update(ctx, cm); err != nil {
		return fmt.Errorf("error while updating the pod environment configmap in service cluster: %w", err)
	}

	return nil
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
	localStandbySecretName := "standby." + instance.ToPeripheralResourceName() + ".credentials"
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

	// Check if secrets exist in remote CONTROL Cluster
	remoteSecretName := instance.Spec.PostgresConnection.ConnectionSecretName
	remoteSecretNamespace := instance.ObjectMeta.Namespace
	remoteSecret := &corev1.Secret{}
	r.Log.Info("fetching remote standby secret", "namespace", remoteSecretNamespace, "name", remoteSecretName)
	if err := r.CtrlClient.Get(ctx, types.NamespacedName{Namespace: remoteSecretNamespace, Name: remoteSecretName}, remoteSecret); err != nil {
		// we cannot read the secret given in the configuration, so we cannot continue!
		return fmt.Errorf("error while fetching remote standby secret from control plane: %w", err)
	}

	// copy ALL secrets...
	for username := range remoteSecret.Data {
		r.Log.Info("creating local secret", "username", username)

		currentSecretName := strings.ReplaceAll(username, "_", "-") + "." + instance.ToPeripheralResourceName() + ".credentials"
		postgresSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      currentSecretName,
				Namespace: localSecretNamespace,
				Labels:    map[string]string(instance.ToZalandoPostgresqlMatchingLabels()),
			},
			Data: map[string][]byte{
				"username": []byte(username),
				"password": remoteSecret.Data[username],
			},
		}

		if err := r.SvcClient.Create(ctx, postgresSecret); err != nil {
			return fmt.Errorf("error while creating local secrets in service cluster: %w", err)
		}
		r.Log.Info("created local secret", "secret", postgresSecret)
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
	opts := []client.ListOption{
		client.InNamespace(instance.ToPeripheralResourceNamespace()),
		client.MatchingLabels{"spilo-role": "master"},
	}
	if err := r.SvcClient.List(ctx, pods, opts...); err != nil {
		r.Log.Info("could not query pods, requeuing")
		return err
	}
	if len(pods.Items) == 0 {
		r.Log.Info("no master pod ready, requeuing")
		// TODO return proper error
		return errors.New("no master pods found")
	}
	podIP := pods.Items[0].Status.PodIP
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
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
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

	pes := &corev1.Service{}

	if err := r.SetName(pes, postgresExporterServiceName); err != nil {
		return fmt.Errorf("error while setting the name of the postgres-exporter service to %v: %w", namespace, err)
	}
	if err := r.SetNamespace(pes, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter service to %v: %w", namespace, err)
	}
	labels := map[string]string{
		// "application": "spilo", // TODO check if we still need that label, IsOperatorDeletable won't work anymore if we set it.
		"app": "postgres-exporter",
	}
	if err := r.SetLabels(pes, labels); err != nil {
		return fmt.Errorf("error while setting the labels of the postgres-exporter service to %v: %w", labels, err)
	}
	annotations := map[string]string{
		postgresExporterServiceTenantAnnotationName:    in.Spec.Tenant,
		postgresExporterServiceProjectIDAnnotationName: in.Spec.ProjectID,
	}
	if err := r.SetAnnotations(pes, annotations); err != nil {
		return fmt.Errorf("error while setting the annotations of the postgres-exporter service to %v: %w", annotations, err)
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
		"application": "spilo",
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

// createOrUpdateExporterSidecarServiceMonitor ensures the servicemonitors for the sidecars exist
func (r *PostgresReconciler) createOrUpdateExporterSidecarServiceMonitor(ctx context.Context, namespace string, in *pg.Postgres) error {
	log := r.Log.WithValues("namespace", namespace)

	pesm := &coreosv1.ServiceMonitor{}

	// TODO what's the correct name?
	if err := r.SetName(pesm, postgresExporterServiceName); err != nil {
		return fmt.Errorf("error while setting the name of the postgres-exporter servicemonitor to %v: %w", namespace, err)
	}
	if err := r.SetNamespace(pesm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter servicemonitor to %v: %w", namespace, err)
	}
	labels := map[string]string{
		"app":     "postgres-exporter",
		"release": "prometheus",
	}
	if err := r.SetLabels(pesm, labels); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter servicemonitor to %v: %w", namespace, err)
	}
	annotations := map[string]string{
		postgresExporterServiceTenantAnnotationName:    in.Spec.Tenant,
		postgresExporterServiceProjectIDAnnotationName: in.Spec.ProjectID,
	}
	if err := r.SetAnnotations(pesm, annotations); err != nil {
		return fmt.Errorf("error while setting the annotations of the postgres-exporter service monitor to %v: %w", annotations, err)
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

	s := &corev1.Service{}
	if err := r.SetName(s, postgresExporterServiceName); err != nil {
		return fmt.Errorf("error while setting the name of the postgres-exporter service to delete to %v: %w", postgresExporterServiceName, err)
	}
	if err := r.SetNamespace(s, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter service to delete to %v: %w", namespace, err)
	}
	if err := r.SvcClient.Delete(ctx, s); err != nil {
		return fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
	}
	log.Info("postgres-exporter service deleted")

	return nil
}
