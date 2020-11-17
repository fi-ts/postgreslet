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
	"time"

	"github.com/go-logr/logr"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	_ = context.Background()
	_ = r.Log.WithValues("postgres", req.NamespacedName)

	instance := &databasev1.Postgres{}
	if err := r.Get(context.Background(), req.NamespacedName, instance); err != nil {
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

	if !instance.Status.Initiated {
		newZInstance, err := toZInstance(instance)
		log.Print(newZInstance)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error while creating the local postgresql in GO-code: %v", err)
		}
		if err := r.Create(context.Background(), newZInstance); err != nil {
			return ctrl.Result{}, fmt.Errorf("error while creating CRD postgresql: %v", err)
		}
		instance.Status.Initiated = true
		if err := r.Status().Update(context.Background(), instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("error while updating the status: %v", err)
		}
	}

	zInstance := &zalando.Postgresql{}
	if err := r.Get(context.Background(), *toZKey(instance), zInstance); err != nil {
		if errors.IsNotFound(err) {
			return Requeue, nil
		}
		return ctrl.Result{}, err
	}
	instance.Status.Description = zInstance.Status.PostgresClusterStatus
	if err := r.Status().Update(context.Background(), instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("error while updating the status: %v", err)
	}

	return ctrl.Result{}, nil
}

// Map to zalando CRD
func toZInstance(in *databasev1.Postgres) (*zalando.Postgresql, error) {
	return &zalando.Postgresql{
		ObjectMeta: meta.ObjectMeta{
			Name:      "acid-minimal-cluster",
			Namespace: in.Namespace,
		},
		Spec: zalando.PostgresSpec{
			Clone: zalando.CloneDescription{
				ClusterName: "",
			},
			Databases:         map[string]string{"foo": "zalando"},
			NumberOfInstances: in.Spec.NumberOfInstances,
			Resources: zalando.Resources{
				ResourceRequests: zalando.ResourceDescription{
					CPU:    in.Spec.Size.CPU,
					Memory: "2Gi",
				},
				ResourceLimits: zalando.ResourceDescription{
					CPU:    in.Spec.Size.CPU,
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
				PgVersion:  "12",
			},
			ServiceAnnotations: map[string]string{},
			StandbyCluster: &zalando.StandbyDescription{
				S3WalPath: "",
			},
			TeamID: "acid",
			TLS: &zalando.TLSDescription{
				SecretName: "",
			},
			Users: map[string]zalando.UserFlags{
				"zalando": {"createdb", "superuser"},
			},
			Volume: zalando.Volume{
				Size: "1Gi",
			},
		}}, nil
}
func toZKey(in *databasev1.Postgres) *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: in.Namespace,
		Name:      toZName(in),
	}
}
func toZName(in *databasev1.Postgres) string {
	return in.Spec.ProjectID + "-" + in.Name
}
func (r *PostgresReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1.Postgres{}).
		Complete(r)
}
