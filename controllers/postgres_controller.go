/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pg "github.com/fi-ts/postgres-controller/api/v1"
	"github.com/fi-ts/postgres-controller/pkg/yamlmanager"
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
	*yamlmanager.YAMLManager
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
	k := z.ToKey()

	// Delete
	if instance.IsBeingDeleted() {
		log.Info("deleting owned zalando postgresql")
		if err := r.deleteZPostgresql(ctx, k); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.UninstallUnstructured(instance.Spec.ZalandoDependencies); err != nil {
			return ctrl.Result{}, fmt.Errorf("error while uninstalling zalando dependencies: %v", err)
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
	rawZ := &zalando.Postgresql{}
	if err := r.Service.Get(ctx, *k, rawZ); err != nil {
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
		log.Info("updating zalando postgresql", "ns/name", k)
		patch := client.MergeFrom(rawZ.DeepCopy())
		patchRawZ(rawZ, instance)
		if err := r.Service.Patch(ctx, rawZ, patch); err != nil {
			log.Error(err, "error while updating zalando postgresql ", "ns/name", k)
			return requeue, err
		}
		log.Info("zalando postgresql updated", "ns/name", k)
	}

	// Update status will be handled by the StatusReconciler, based on the Zalando Status

	return ctrl.Result{}, nil
}

// SetupWithManager informs mgr when this reconciler should be called.
func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pg.Postgres{}).
		Complete(r)
}

func (r *PostgresReconciler) createZalandoPostgresql(ctx context.Context, z *pg.ZalandoPostgres) (ctrl.Result, error) {
	log := r.Log.WithValues("zalando postgresql", z.ToKey())

	// Make sure the namespace exists in the worker-cluster. // todo: Make sure it happens in the worker-cluster.
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

// todo: Make sure it takes effect in the service-node.
// ensureZalandoDependencies makes sure Zalando resources are installed in the service-node.
func (r *PostgresReconciler) ensureZalandoDependencies(ctx context.Context, pg *pg.Postgres) error {
	namespace := pg.Spec.ProjectID
	isInstalled, err := r.isZalandoDependenciesInstalled(ctx, namespace)
	if err != nil {
		return fmt.Errorf("error while querying if zalando dependencies are installed: %v", err)
	}
	if !isInstalled {
		objs, err := r.InstallYAML(ctx, namespace, pg.Spec.Backup.S3BucketURL)
		if err != nil {
			return fmt.Errorf("error while installing zalando dependencies: %v", err)
		}
		if err := r.patchZalandoDependencies(ctx, pg, objs); err != nil {
			return fmt.Errorf("error while patching zalando dependencies in postgres: %v", err)
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

func (r *PostgresReconciler) isZalandoDependenciesInstalled(ctx context.Context, namespace string) (bool, error) {
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{"name": "postgres-operator"}, // todo: Fetch labels programmatically.
	}
	if err := r.List(ctx, pods, opts...); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return true, nil
}

func (r *PostgresReconciler) patchZalandoDependencies(ctx context.Context, pg *pg.Postgres, objs []runtime.Object) error {
	patch := client.MergeFrom(pg.DeepCopy())
	if err := pg.SetZalandoDependencies(objs); err != nil {
		return fmt.Errorf("error while setting zalando dependencies: %v", err)
	}
	if err := r.Patch(ctx, pg, patch); err != nil {
		return fmt.Errorf("error while patching postgres: %v", err)
	}
	return nil
}

// todo: shift the logic to postgresql.
func (r *PostgresReconciler) deleteZPostgresql(ctx context.Context, k *types.NamespacedName) error {
	log := r.Log.WithValues("zalando postgrsql", k)
	rawZ := &zalando.Postgresql{}
	if err := r.Service.Get(ctx, *k, rawZ); err != nil {
		return fmt.Errorf("error while fetching zalando postgresql to delete: %v", err)
	}

	if err := r.Service.Delete(ctx, rawZ); err != nil {
		return fmt.Errorf("error while deleting zalando postgresql: %v", err)
	}

	log.Info("zalando postgresql deleted")
	return nil
}

func patchRawZ(out *zalando.Postgresql, in *pg.Postgres) {
	out.Spec.NumberOfInstances = in.Spec.NumberOfInstances

	// todo: Check if the validation should be performed here.
	out.Spec.PostgresqlParam.PgVersion = in.Spec.Version

	out.Spec.ResourceRequests.CPU = in.Spec.Size.CPU

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

	// todo: in.Spec.Backup, in.Spec.AccessList
}
