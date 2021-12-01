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
	goerrors "errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"inet.af/netaddr"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"

	firewall "github.com/metal-stack/firewall-controller/api/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
		if errors.IsNotFound(err) {
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

	if instance.IsPrimaryLeader() {
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
	if err := r.createOrUpdateCWNP(ctx, instance, int(port)); err != nil {
		r.recorder.Event(instance, "Warning", "Error", "failed to create or update ClusterwideNetworkPolicy")
		return ctrl.Result{}, fmt.Errorf("unable to create or update corresponding CRD ClusterwideNetworkPolicy: %w", err)
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
	c := &v1.ConfigMap{}
	if err := r.SvcClient.Get(ctx, cns, c); err != nil {
		// configmap with configuration does not exists, nothing we can do here...
		log.Info("could not fetch config for sidecars")
		c = nil
	}

	// Get zalando postgresql and create one if none.
	rawZ, err := r.getZalandoPostgresql(ctx, instance)
	if err != nil {
		// errors other than `NotFound`
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to fetch zalando postgresql: %w", err)
		}

		u, err := instance.ToUnstructuredZalandoPostgresql(nil, c, r.StorageClass, r.PgParamBlockList)
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

	u, err := instance.ToUnstructuredZalandoPostgresql(rawZ, c, r.StorageClass, r.PgParamBlockList)
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

	// fetch secret
	backupSecret := &v1.Secret{}
	backupNamespace := types.NamespacedName{
		Name:      p.Spec.BackupSecretRef,
		Namespace: p.Namespace,
	}
	if err := r.CtrlClient.Get(ctx, backupNamespace, backupSecret); err != nil {
		return fmt.Errorf("error while getting the backup secret from control plane cluster: %w", err)
	}

	backupConfigJSON, ok := backupSecret.Data[pg.BackupConfigKey]
	if !ok {
		return fmt.Errorf("no backupConfig stored in the secret")
	}
	var backupConfig pg.BackupConfig
	err := json.Unmarshal(backupConfigJSON, &backupConfig)
	if err != nil {
		return fmt.Errorf("unable to unmarshal backupconfig:%w", err)
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

	cm := &v1.ConfigMap{}
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
// createOrUpdateCWNP will create an ingress firewall rule on the firewall in front of the k8s cluster
// based on the spec.AccessList.SourceRanges given.
func (r *PostgresReconciler) createOrUpdateCWNP(ctx context.Context, in *pg.Postgres, port int) error {
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

	if in.Spec.PostgresConnectionInfo == nil {
		return nil
	}

	// Create CWNP if standby is configured (independant of the current role)
	standbyIngressCWNPName := in.ToPeripheralResourceName() + "-standby-ingress"
	standbyIngressCWNP := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyIngressCWNPName, Namespace: policy.Namespace}}

	//
	// Create ingress rule from pre-configured CIDR to this DBs port
	//
	standbyClusterIngressIPBlocks := []networkingv1.IPBlock{}
	for _, cidr := range r.StandbyClustersSourceRanges {
		remoteServiceClusterCIDR, err := netaddr.ParseIPPrefix(cidr)
		if err != nil {
			return fmt.Errorf("unable to parse standby host ip %s: %w", in.Spec.PostgresConnectionInfo.ConnectionIP, err)
		}
		standbyClusterIPs := networkingv1.IPBlock{
			CIDR: remoteServiceClusterCIDR.String(),
		}
		standbyClusterIngressIPBlocks = append(standbyClusterIngressIPBlocks, standbyClusterIPs)
	}

	// Add Port to CWNP (if known)
	tcp := corev1.ProtocolTCP
	ingressTargetPorts := []networkingv1.NetworkPolicyPort{}
	if in.Status.Socket.Port != 0 {
		portObj := intstr.FromInt(int(in.Status.Socket.Port))
		ingressTargetPorts = append(ingressTargetPorts, networkingv1.NetworkPolicyPort{Port: &portObj, Protocol: &tcp})
	}
	standbyIngressCWNP.Spec.Ingress = []firewall.IngressRule{
		{Ports: ingressTargetPorts, From: standbyClusterIngressIPBlocks},
	}

	key2 := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyIngressCWNPName, Namespace: policy.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, standbyIngressCWNP, func() error {
		key2.Spec.Ingress = standbyIngressCWNP.Spec.Ingress
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy standby ingress ClusterwideNetworkPolicy: %w", err)
	}

	//
	// Create egress rule to StandbyCluster CIDR and ConnectionPort
	//
	standbyClusterEgressIPBlocks := []networkingv1.IPBlock{}
	if in.Spec.PostgresConnectionInfo.ConnectionIP != "" {
		remoteServiceClusterCIDR, err := netaddr.ParseIPPrefix(in.Spec.PostgresConnectionInfo.ConnectionIP + "/32")
		if err != nil {
			return fmt.Errorf("unable to parse standby host ip %s: %w", in.Spec.PostgresConnectionInfo.ConnectionIP, err)
		}
		standbyClusterIPs := networkingv1.IPBlock{
			CIDR: remoteServiceClusterCIDR.String(),
		}
		standbyClusterEgressIPBlocks = append(standbyClusterEgressIPBlocks, standbyClusterIPs)
	}

	standbyEgressCWNPName := in.ToPeripheralResourceName() + "-standby-egress"
	standbyEgressCWNP := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyEgressCWNPName, Namespace: policy.Namespace}}
	// Add Port to CWNP
	egressTargetPorts := []networkingv1.NetworkPolicyPort{}
	if in.Spec.PostgresConnectionInfo.ConnectionPort != 0 {
		portObj := intstr.FromInt(int(in.Spec.PostgresConnectionInfo.ConnectionPort))
		egressTargetPorts = append(egressTargetPorts, networkingv1.NetworkPolicyPort{Port: &portObj, Protocol: &tcp})
	}
	standbyEgressCWNP.Spec.Egress = []firewall.EgressRule{
		{Ports: egressTargetPorts, To: standbyClusterEgressIPBlocks},
	}

	key3 := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyEgressCWNPName, Namespace: policy.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.SvcClient, standbyEgressCWNP, func() error {
		key3.Spec.Egress = standbyEgressCWNP.Spec.Egress
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy standby egress ClusterwideNetworkPolicy: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) deleteCWNP(ctx context.Context, in *pg.Postgres) error {
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
		return nil, errors.NewNotFound(zalando.Resource("postgresql"), "")
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
	if instance.IsPrimaryLeader() {
		// nothing is configured, or we are the leader. nothing to do.
		return nil
	}

	//  TODO check if instance.Spec.PostgresConnectionInfo.ConnectionSecretName is defined

	// Check if secrets exist local in SERVICE Cluster
	localStandbySecretName := "standby." + instance.ToPeripheralResourceName() + ".credentials"
	localSecretNamespace := instance.ToPeripheralResourceNamespace()
	localStandbySecret := &corev1.Secret{}
	r.Log.Info("checking for local standby secret", "namespace", localSecretNamespace, "name", localStandbySecretName)
	if err := r.SvcClient.Get(ctx, types.NamespacedName{Namespace: localSecretNamespace, Name: localStandbySecretName}, localStandbySecret); err != nil {
		// we got an error other than not found, so we cannot continue!
		if !errors.IsNotFound(err) {
			return fmt.Errorf("error while fetching local stadnby secret from service cluster: %w", err)
		}

		r.Log.Info("no local standby secret found, continuing to create one")

		// Check if secrets exist in remote CONTROL Cluster
		remoteSecretName := instance.Spec.PostgresConnectionInfo.ConnectionSecretName
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

	} else {
		r.Log.Info("local standby secret found, no action needed")
	}

	return nil

}

func (r *PostgresReconciler) updatePatroniConfig(ctx context.Context, instance *pg.Postgres) error {
	// Finally, send a POST to to the database with the correct config
	if instance.Spec.PostgresConnectionInfo == nil {
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
		return goerrors.New("no master pods found")
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
	if instance.IsPrimaryLeader() {
		request = PatroniConfigRequest{
			StandbyCluster: nil,
		}
		if instance.Spec.PostgresConnectionInfo.SynchronousReplication {
			// enable sync replication
			request.SynchronousNodesAdditional = pointer.String(instance.Spec.PostgresConnectionInfo.ConnectedPostgresID)
		} else {
			// disable sync replication
			request.SynchronousNodesAdditional = nil
		}
	} else {
		// TODO check values first
		request = PatroniConfigRequest{
			StandbyCluster: &PatroniStandbyCluster{
				CreateReplicaMethods: []string{"basebackup_fast_xlog"},
				Host:                 instance.Spec.PostgresConnectionInfo.ConnectionIP,
				Port:                 int(instance.Spec.PostgresConnectionInfo.ConnectionPort),
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
