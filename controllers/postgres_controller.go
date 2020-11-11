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

	"github.com/go-logr/logr"
	acidv1 "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	// v1 "k8s.io/client-go/1.5/pkg/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	databasev1 "github.com/fi-ts/postgres-controller/api/v1"
)

// PostgresReconciler reconciles a Postgres object
type PostgresReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=database.fits.cloud,resources=postgres/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresql,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresql/status,verbs=get;update;patch

func (r *PostgresReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	_ = r.Log.WithValues("postgres", req.NamespacedName)

	instance := &databasev1.Postgres{}
	if err := r.Get(context.Background(), req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if instance.IsBeingDeleted() {
		// Delete the instance.
	}

	// Evaluate the status of the instance.
	// One circumstance: Create
	newInstance := &acidv1.Postgresql{
		// ObjectMeta: v1.ObjectMeta{
		// 	Name: instance.Spec.TeamID + "-" + instance.Name,
		// },
		Spec: acidv1.PostgresSpec{
			TeamID:            instance.Spec.TeamID,
			NumberOfInstances: instance.Spec.NumberOfInstances,
			PostgresqlParam: acidv1.PostgresqlParam{
				PgVersion: instance.Spec.Version,
			},
		},
	}
	newInstance.Name = instance.Spec.TeamID + "-" + instance.Name
	if err := r.Create(context.Background(), newInstance); err != nil {

	}

	return ctrl.Result{}, nil
}

func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1.Postgres{}).
		Complete(r)
}
