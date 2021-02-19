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

	// corev1 "k8s.io/api/core/v1"

	firewall "github.com/metal-stack/firewall-controller/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

	// Delete
	if instance.IsBeingDeleted() {
		matchingLabels := instance.ToZalandoPostgresqlMatchingLabels()
		namespace := instance.ToPeripheralResourceNamespace()

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

		deletable, err := r.IsOperatorDeletable(ctx, namespace)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error while checking if the operator is idle: %v", err)
		}
		if !deletable {
			log.Info("operator not deletable")
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

	if err := r.createOrUpdateZalandoPostgresql(ctx, instance, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create or update zalando postgresql: %w", err)
	}

	if err := r.CreateSvcLBIfNone(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	// Check if socket port is ready
	port := instance.Status.Socket.Port
	if port == 0 {
		log.Info("socket port not ready")
		return ctrl.Result{Requeue: true}, nil
	}

	// Update status will be handled by the StatusReconciler, based on the Zalando Status
	if err := r.createOrUpdateCWNP(ctx, instance, int(port)); err != nil {
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

func (r *PostgresReconciler) createOrUpdateZalandoPostgresql(ctx context.Context, instance *pg.Postgres, log logr.Logger) error {
	// Get zalando postgresql and create one if none.
	rawZ, err := r.getZalandoPostgresql(ctx, instance)
	if err != nil {
		// errors other than `NotFound`
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to fetch zalando postgresql: %w", err)
		}

		u, err := instance.ToUnstructuredZalandoPostgresql(nil)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
		}

		if err := r.Service.Create(ctx, u); err != nil {
			return fmt.Errorf("failed to create zalando postgresql: %w", err)
		}
		log.Info("zalando postgresql created", "zalando postgresql", u)

		return nil
	}

	// Update zalando postgresql
	mergeFrom := client.MergeFrom(rawZ.DeepCopy())

	u, err := instance.ToUnstructuredZalandoPostgresql(rawZ)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
	}
	if err := r.Service.Patch(ctx, u, mergeFrom); err != nil {
		return fmt.Errorf("failed to update zalando postgresql: %w", err)
	}
	log.Info("zalando postgresql updated", "zalando postgresql", u)

	return nil
}

// func (r *PostgresReconciler) createZalandoPostgresql(ctx context.Context, instance *pg.Postgres) error {
// 	z := instance.ToZalandoPostgres()
// 	namespacedName := types.NamespacedName{Namespace: z.Namespace, Name: z.Name}
// 	log := r.Log.WithValues("ns/name", namespacedName)

// 	// todo: Remove this. Ensuring namaspace is covered in operator intallation.
// 	// // Make sure the namespace exists in the worker-cluster.
// 	// ns := z.Namespace
// 	// if err := r.Service.Get(ctx, client.ObjectKey{Name: ns}, &corev1.Namespace{}); err != nil {
// 	// 	// errors other than `not found`
// 	// 	if !errors.IsNotFound(err) {
// 	// 		return err
// 	// 	}

// 	// 	// Create the namespace.
// 	// 	nsObj := &corev1.Namespace{}
// 	// 	nsObj.Name = ns
// 	// 	if err = r.Service.Create(ctx, nsObj); err != nil {
// 	// 		return err
// 	// 	}
// 	// }

// 	u, err := z.ToUnstructured()
// 	if err != nil {
// 		log.Error(err, "error while converting to unstructured")
// 		return err
// 	}

// 	if err := r.Service.Create(ctx, u); err != nil {
// 		log.Error(err, "error while creating zalando postgresql")
// 		return err
// 	}

// 	return nil
// }

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
	if err := r.Service.List(ctx, zpl, opts...); err != nil {
		return nil, err
	}

	return zpl.Items, nil
}
