/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	corev1 "k8s.io/api/core/v1"

	firewall "github.com/metal-stack/firewall-controller/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
)

// requeue defines in how many seconds a requeue should happen
var requeue = ctrl.Result{
	Requeue:      true,
	RequeueAfter: 30 * time.Second,
}

// PostgresReconciler reconciles a Postgres object
type PostgresReconciler struct {
	client.Client
	Service             client.Client
	Log                 logr.Logger
	Scheme              *runtime.Scheme
	PartitionID, Tenant string
	*operatormanager.OperatorManager
	*lbmanager.LBManager
}

// Reconcile is the entry point for postgres reconciliation.
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;list;watch
func (r *PostgresReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("postgres", req.NamespacedName)

	log.Info("fetchting postgres")
	instance := &pg.Postgres{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("postgres fetched", "postgres", instance)

	if !r.isManagedByUs(instance) {
		log.Info("object should be managed by another postgreslet, ignored.")
		return ctrl.Result{}, nil
	}

	z := instance.ToZalandoPostgres()

	matchingLabels := client.MatchingLabels{pg.LabelName: string(instance.UID)}
	// we use z.Namespace so the calculation of the name is encapsuled in ToZalandoPostgres()
	namespace := z.Namespace

	// Delete
	if instance.IsBeingDeleted() {
		if err := r.deleteCWNP(ctx, instance); client.IgnoreNotFound(err) != nil { // todo: remove ignorenotfound
			return ctrl.Result{}, err
		}
		log.Info("corresponding CRD ClusterwideNetworkPolicy deleted")

		if err := r.DeleteSvcLB(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("corresponding Service of type LoadBalancer deleted")

		log.Info("deleting owned zalando postgresql")

		if err := r.deleteZPostgresqlByLabels(ctx, matchingLabels, namespace); err != nil {
			return ctrl.Result{}, err
		}

		isIdle, err := r.IsOperatorDeletable(ctx, namespace)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error while checking if the operator is idle: %v", err)
		}
		if !isIdle {
			log.Info("operator is not idle")
			return ctrl.Result{Requeue: true}, nil
		}
		if err := r.UninstallOperator(ctx, namespace); err != nil {
			return ctrl.Result{}, fmt.Errorf("error while uninstalling operator: %v", err)
		}

		instance.RemoveFinalizer(pg.PostgresFinalizerName)
		if err := r.Update(ctx, instance); err != nil {
			return requeue, err
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if none.
	if !instance.HasFinalizer(pg.PostgresFinalizerName) {
		log.Info("finalizer being added")
		instance.AddFinalizer(pg.PostgresFinalizerName)
		if err := r.Update(ctx, instance); err != nil {
			return requeue, fmt.Errorf("error while adding finalizer: %v", err)
		}
		log.Info("finalizer added")
		return ctrl.Result{}, nil
	}

	// Check if zalando dependencies are installed. If not, install them.
	if err := r.ensureZalandoDependencies(ctx, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while ensuring Zalando dependencies: %v", err)
	}

	// Get zalando postgresql and create one if none.
	rawZ, err := r.getZPostgresql(ctx, matchingLabels, namespace)
	if err != nil {
		log.Info("unable to fetch zalando postgresql", "error", err)
		// errors other than `NotFound`
		if !errors.IsNotFound(err) {
			return requeue, err
		}
		log.Info("creating zalando postgresql", "ns/name", z)

		return r.createZalandoPostgresql(ctx, z)
	}

	// Update zalando postgresql.
	if rawZ.Name != "" {
		namespacedName := types.NamespacedName{Namespace: rawZ.Namespace, Name: rawZ.Name}
		log.Info("updating zalando postgresql", "ns/name", namespacedName)
		patch := client.MergeFrom(rawZ.DeepCopy())
		patchRawZ(rawZ, instance)
		if err := r.Service.Patch(ctx, rawZ, patch); err != nil {
			log.Error(err, "error while updating zalando postgresql", "ns/name", namespacedName)
			return requeue, err
		}
		log.Info("zalando postgresql updated", "ns/name", namespacedName)
	}

	if err := r.CreateSvcLBIfNone(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	// todo: Check the port. The default port of postgres is used.
	// Update status will be handled by the StatusReconciler, based on the Zalando Status
	if err := r.createOrUpdateCWNP(ctx, instance, 5432); err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to create or update corresponding CRD ClusterwideNetworkPolicy: %W", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager informs mgr when this reconciler should be called.
func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pg.Postgres{}).
		Complete(r)
}

func (r *PostgresReconciler) createZalandoPostgresql(ctx context.Context, z *pg.ZalandoPostgres) (ctrl.Result, error) {
	namespacedName := types.NamespacedName{Namespace: z.Namespace, Name: z.Name}
	log := r.Log.WithValues("ns/name", namespacedName)

	// Make sure the namespace exists in the worker-cluster.
	ns := z.Namespace
	if err := r.Service.Get(ctx, client.ObjectKey{Name: ns}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = ns
		if err = r.Service.Create(ctx, nsObj); err != nil {
			return ctrl.Result{}, err
		}
	}

	u, err := z.ToUnstructured()
	if err != nil {
		log.Error(err, "error while converting to unstructured")
		return ctrl.Result{}, err
	}

	if err := r.Service.Create(ctx, u); err != nil {
		log.Error(err, "error while creating zalando postgresql")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// ensureZalandoDependencies makes sure Zalando resources are installed in the service-cluster.
func (r *PostgresReconciler) ensureZalandoDependencies(ctx context.Context, pg *pg.Postgres) error {
	namespace := pg.ToPeripheralResourceNamespace()
	isInstalled, err := r.IsOperatorInstalled(ctx, namespace)
	if err != nil {
		return fmt.Errorf("error while querying if zalando dependencies are installed: %v", err)
	}
	if !isInstalled {
		_, err := r.InstallOperator(ctx, namespace, pg.Spec.Backup.S3BucketURL)
		if err != nil {
			return fmt.Errorf("error while installing zalando dependencies: %v", err)
		}
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

	for _, rawZ := range items {
		log := r.Log.WithValues("zalando postgrsql", rawZ)
		if err := r.Service.Delete(ctx, &rawZ); err != nil {
			return fmt.Errorf("error while deleting zalando postgresql: %v", err)
		}
		log.Info("zalando postgresql deleted")
	}

	return nil
}

func patchRawZ(out *zalando.Postgresql, in *pg.Postgres) {
	if in.HasSourceRanges() {
		out.Spec.AllowedSourceRanges = in.Spec.AccessList.SourceRanges
	}

	out.Spec.NumberOfInstances = in.Spec.NumberOfInstances

	// todo: Check if the validation should be performed here.
	out.Spec.PostgresqlParam.PgVersion = in.Spec.Version

	out.Spec.ResourceRequests.CPU = in.Spec.Size.CPU
	out.Spec.ResourceRequests.Memory = in.Spec.Size.Memory

	// todo: Check if the validation should be performed here.
	out.Spec.Volume.Size = in.Spec.Size.StorageSize

	out.Spec.MaintenanceWindows = func() []zalando.MaintenanceWindow {
		if in.Spec.Maintenance == nil {
			return nil
		}
		isEvery := in.Spec.Maintenance.Weekday == pg.All
		return []zalando.MaintenanceWindow{
			{
				Everyday: isEvery,
				Weekday: func() time.Weekday {
					if isEvery {
						return time.Weekday(0)
					}
					return time.Weekday(in.Spec.Maintenance.Weekday)
				}(),
				StartTime: in.Spec.Maintenance.TimeWindow.Start,
				EndTime:   in.Spec.Maintenance.TimeWindow.End,
			},
		}
	}()

	// todo: in.Spec.Backup
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
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Service, key, func() error {
		key.Spec.Ingress = policy.Spec.Ingress
		return nil
	}); err != nil {
		return fmt.Errorf("unable to deploy CRD ClusterwideNetworkPolicy: %w", err)
	}

	return nil
}

func (r *PostgresReconciler) deleteCWNP(ctx context.Context, in *pg.Postgres) error {
	policy := &firewall.ClusterwideNetworkPolicy{}
	policy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	policy.Name = in.ToPeripheralResourceName()
	if err := r.Service.Delete(ctx, policy); err != nil {
		return fmt.Errorf("unable to delete CRD ClusterwideNetworkPolicy %v: %w", policy.Name, err)
	}
	return nil
}

// Returns *only one* Zalndo Postgresql resource with the given label, returns an error if not unique.
func (r *PostgresReconciler) getZPostgresql(ctx context.Context, matchingLabel client.MatchingLabels, namespace string) (*zalando.Postgresql, error) {
	items, err := r.getZPostgresqlByLabels(ctx, matchingLabel, namespace)
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
	if err := r.Service.List(ctx, zpl, opts...); err != nil {
		return nil, err
	}

	return zpl.Items, nil
}
