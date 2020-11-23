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
	"time"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	databasev1 "github.com/fi-ts/postgres-controller/api/v1"
)

// Requeue defines in how many seconds a requeue should happen
var Requeue = ctrl.Result{
	Requeue:      true,
	RequeueAfter: 30 * time.Second,
}

// PostgresReconciler reconciles a Postgres object
type PostgresReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;list;watch

func (r *PostgresReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("postgres", req.NamespacedName)

	instance := &databasev1.Postgres{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// TODO implement later once postgreslet is in place
	// if instance.Spec.PartitionID != myPartition {
	// 	return ctrl.Result{}, nil
	// }

	// if tenantOnly != "" && instance.Spec.Tenant != tenantOnly {
	// 	return ctrl.Result{}, nil
	// }

	if instance.IsBeingDeleted() {
		// Delete the instance.
	}

	rawZ := &zalando.Postgresql{}
	z := instance.ToZPostgres()
	// Add `instance` (owner) to the metadata of `z` (owned).
	controllerutil.SetControllerReference(instance, z, r.Scheme)
	k := z.ToKey()
	u, err := z.ToUnstructured()
	if err != nil {
		log.Error(err, "error while converting to unstructured")
		return ctrl.Result{}, err
	}

	// Get zalando postgresql and create one if none.
	if err := r.Client.Get(ctx, *k, rawZ); err != nil {
		log.Info("unable to fetch zalando postgresql", "error", err)
		// errors other than `NotFound`
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("creating zalando postgresql", "ns/name", z)

		// todo: Create a ns if none.

		if err := r.Client.Create(ctx, u); err != nil {
			log.Error(err, "error while creating zalando postgresql", "ns/name", k)
			return ctrl.Result{}, err
		}
	}

	// Update zalando postgresql.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		log.Info("updating zalando postgresql", "ns/name", k)

		// Complete ObjectMeta.
		z.ObjectMeta = rawZ.ObjectMeta
		z.Status.PostgresClusterStatus = rawZ.Status.PostgresClusterStatus
		u, err := z.ToUnstructured()
		if err != nil {
			log.Error(err, "error while converting to unstructured")
			return err
		}
		if err := r.Client.Update(ctx, u); err != nil {
			log.Error(err, "error while updating zalando postgresql ", "ns/name", k)
			return err
		}
		log.Info("zalando postgresql updated", "ns/name", k)
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Update status.
	newStatus := rawZ.Status.PostgresClusterStatus
	instance.Status.Description = newStatus
	if err := r.Status().Update(ctx, instance); err != nil {
		log.Error(err, "error while updating postgres status")
		return ctrl.Result{}, err
	}
	log.Info("postgres status updated successfully", "status", newStatus)

	return ctrl.Result{}, nil
}

func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1.Postgres{}).
		Owns(&zalando.Postgresql{}).
		Complete(r)
}
