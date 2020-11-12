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
	"log"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	newZInstance, err := addDefaultValue(&zalando.Postgresql{
		ObjectMeta: meta.ObjectMeta{
			Name:      instance.Spec.TeamID + "-" + instance.Name,
			Namespace: instance.Namespace,
		},
		Spec: zalando.PostgresSpec{
			Clone: zalando.CloneDescription{
				ClusterName: "cluster-name-example",
			},
			NumberOfInstances: instance.Spec.NumberOfInstances,
			Resources: zalando.Resources{
				ResourceLimits: zalando.ResourceDescription{
					CPU:    "1",
					Memory: "2Gi",
				},
				ResourceRequests: zalando.ResourceDescription{
					CPU:    "1",
					Memory: "2Gi",
				},
			},
			Patroni: zalando.Patroni{
				InitDB: map[string]string{},
				PgHba:  []string{},
				Slots:  map[string]map[string]string{},
			},
			PodAnnotations: map[string]string{},
			PostgresqlParam: zalando.PostgresqlParam{
				Parameters: map[string]string{},
				PgVersion:  instance.Spec.Version,
			},
			ServiceAnnotations: map[string]string{},
			StandbyCluster: &zalando.StandbyDescription{
				S3WalPath: "s3-wal-path-example",
			},
			TeamID: instance.Spec.TeamID,
			TLS: &zalando.TLSDescription{
				SecretName: "secret-name-example",
			},
			Users: map[string]zalando.UserFlags{},
			Volume: zalando.Volume{
				Size: "8Gi",
			},
		},
	})
	log.Print(newZInstance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating the local postgresql in GO-code: %v", err)
	}
	if err := r.Create(context.Background(), newZInstance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while creating CRD postgresql: %v", err)
	}
	return ctrl.Result{}, nil
}

// todo: Modify the postgresql object.
func addDefaultValue(before *zalando.Postgresql) (*zalando.Postgresql, error) {
	// after := &zalando.Postgresql{}
	return before, nil
}
func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1.Postgres{}).
		Complete(r)
}
