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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;update;patch

func (r *PostgresReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	lgr := r.Log.WithValues("postgres", req.NamespacedName)

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

	if err := r.createOrUpdate(ctx, lgr, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating CRD postgresql: %v", err)
	}

	// todo: Update the status
	// zInstance := &zalando.Postgresql{}
	// if err := r.Get(context.Background(), *toKey(newZInstance), zInstance); err != nil {
	// 	if errors.IsNotFound(err) {
	// 		return Requeue, nil
	// 	}
	// 	return ctrl.Result{}, err
	// }
	// instance.Status.Description = zInstance.Status.PostgresClusterStatus
	// if err := r.Status().Update(context.Background(), instance); err != nil {
	// 	return ctrl.Result{}, fmt.Errorf("error while updating the status: %v", err)
	// }

	return ctrl.Result{}, nil
}
func (r *PostgresReconciler) createOrUpdate(ctx context.Context, log logr.Logger, p *databasev1.Postgres) error {
	k := p.ToKey()
	log.Info("create or update", "namespaced name", k)

	rawZ := &zalando.Postgresql{}
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Client.Get(ctx, *k, rawZ); err != nil {
			log.Info(err.Error(), "namespaced name", k)
			if errors.IsNotFound(err) {
				log.Info("create", "postgres", p)
				u, err := p.ToZPostgres().ToUnstructured()
				if err != nil {
					return err
				}

				// todo: Create a ns if none.

				if err := r.Client.Create(ctx, u); err != nil {
					log.Error(err, "unable to create", "namespaced name", k)
					return err
				}
				return nil
			}
			return err
		}
		// log.Info("update", "namespaced name", k)
		// if err := r.Client.Update(ctx, obj); err != nil {
		// 	// if err := r.Client.Update(ctx, toUnstructured()); err != nil {
		// 	log.Error(err, "unable to update", "namespaced name", k)
		// 	return err
		// }

		// instance.Status.Description = zInstance.Status.PostgresClusterStatus
		// if err := r.Status().Update(context.Background(), instance); err != nil {
		// 	return ctrl.Result{}, fmt.Errorf("error while updating the status: %v", err)
		// }

		return nil
	})
	return retryErr
}

func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1.Postgres{}).
		Complete(r)
}
