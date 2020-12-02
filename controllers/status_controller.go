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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"

	pg "github.com/fi-ts/postgres-controller/api/v1"
)

// StatusReconciler reconciles a Postgresql object
type StatusReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// Reconcile updates the status of the remote Postgres object based on the status of the local zalando object.
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;update;patch
func (r *StatusReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("postgresql", req.NamespacedName)

	log.Info("fetching postgresql")
	instance := &zalando.Postgresql{}
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("status changed to Deleted")
		return ctrl.Result{}, nil
	}
	log.Info("Got status update", "PostgresClusterStatus", instance.Status.PostgresClusterStatus)

	log.Info("fetching all owners")
	owners := &pg.PostgresList{}
	if err := r.Client.List(ctx, owners); err != nil {
		log.Info("error fetching all owners")
		return ctrl.Result{}, err
	}

	// FIXME currently hardcoded!!!
	derivedOwnerName := "sample-name"
	var owner pg.Postgres
	var ownerFound bool
	for _, owner = range owners.Items {
		// TODO switch to UID
		if owner.Name != derivedOwnerName {
			log.Info("owner not a match", "remote", owner)
			continue
		}
		// TODO also check ProjectID

		log.Info("Found owner", "owner", owner)
		ownerFound = true
		break
	}

	if !ownerFound {
		// TODO no owner found (should never happen...?)
		err := errors.NewNotFound(schema.GroupResource{Group: pg.GroupVersion.Group, Resource: owner.GetObjectKind().GroupVersionKind().Kind}, derivedOwnerName)
		log.Error(err, "Could not find the owner")
		return ctrl.Result{}, err
	}

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh copy of the owner object
		if err := r.Client.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, &owner); err != nil {
			return err
		}
		log.Info("Updating owner", "owner", owner)
		owner.Status.Description = instance.Status.PostgresClusterStatus
		if err := r.Client.Update(ctx, &owner); err != nil {
			log.Error(err, "failed to update owner object")
			return err
		}
		return nil
	})

	if retryErr != nil {
		return ctrl.Result{}, retryErr
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers this controller for reconciliation of Postgresql resources
func (r *StatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&zalando.Postgresql{}).
		Complete(r)
}
