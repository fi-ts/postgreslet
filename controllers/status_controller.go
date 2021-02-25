/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/
package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"

	pg "github.com/fi-ts/postgreslet/api/v1"
)

// StatusReconciler reconciles a Postgresql object
type StatusReconciler struct {
	client.Client
	Control client.Client
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

// Reconcile updates the status of the remote Postgres object based on the status of the local zalando object.
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;update;patch
func (r *StatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
	if err := r.Control.List(ctx, owners); err != nil {
		log.Info("error fetching all owners")
		return ctrl.Result{}, err
	}

	derivedOwnerUID, err := deriveOwnerData(instance)
	if err != nil {
		return ctrl.Result{}, err
	}
	var owner pg.Postgres
	ownerFound := false
	for _, o := range owners.Items {
		if o.UID != derivedOwnerUID {
			continue
		}

		log.Info("Found owner", "owner", o)
		owner = o
		ownerFound = true
		break
	}

	if !ownerFound {
		return ctrl.Result{}, fmt.Errorf("Could not find the owner")
	}

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh copy of the owner object
		if err := r.Control.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, &owner); err != nil {
			return err
		}
		// update the status of the remote object
		owner.Status.Description = instance.Status.PostgresClusterStatus
		// update the reference to the zalando instance in the remote object
		owner.Status.ChildName = instance.Name

		log.Info("Updating owner", "owner", owner)
		if err := r.Control.Status().Update(ctx, &owner); err != nil {
			log.Error(err, "failed to update owner object")
			return err
		}
		return nil
	})

	if retryErr != nil {
		return ctrl.Result{}, retryErr
	}

	lb := &corev1.Service{}
	if err := r.Client.Get(ctx, *owner.ToSvcLBNamespacedName(), lb); err == nil {
		owner.Status.Socket.IP = lb.Spec.LoadBalancerIP
		owner.Status.Socket.Port = lb.Spec.Ports[0].Port

		if err := r.Control.Status().Update(ctx, &owner); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update lbSocket of Postgres: %w", err)
		}
	} else {
		log.Error(err, "failed to fetch the corresponding Service of type LoadBalancer")
	}

	// Fetch the list of operator-generated secrets
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, owner.ToUserPasswordsSecretListOption()...); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("failed to fetch the list of operator generated secrets: %w", err)
	}

	if err := r.createOrUpdateSecret(ctx, &owner, secrets, log); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers this controller for reconciliation of Postgresql resources
func (r *StatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// todo: Better the watch mechanishm on secrets that weren't generated by this controller
	return ctrl.NewControllerManagedBy(mgr).
		For(&zalando.Postgresql{}).
		Complete(r)
}

func (r *StatusReconciler) createOrUpdateSecret(ctx context.Context, in *pg.Postgres, secrets *corev1.SecretList, log logr.Logger) error {
	secret, err := in.ToUserPasswordsSecret(secrets, r.Scheme)
	if err != nil {
		return fmt.Errorf("failed to convert to the secret containing user password pairs: %w", err)
	}

	// placeholder of the secret with the specified namespaced name
	fetched := &corev1.Secret{ObjectMeta: secret.ObjectMeta}
	// todo: update to CreateOrPatch()
	result, err := controllerutil.CreateOrUpdate(ctx, r.Control, fetched, func() error {
		fetched.Data = secret.Data
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update the secret containing user password pairs: %w", err)
	}
	log.Info("secret created or updated", "operation result", result)

	return nil
}

// Extract the UID of the owner object by reading the value of a certain label
func deriveOwnerData(instance *zalando.Postgresql) (types.UID, error) {
	value, ok := instance.ObjectMeta.Labels[pg.UIDLabelName]
	if !ok {
		return "", fmt.Errorf("Could not derive owner reference")
	}
	ownerUID := types.UID(value)
	return ownerUID, nil
}
