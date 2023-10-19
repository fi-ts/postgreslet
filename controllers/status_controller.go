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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"

	pg "github.com/fi-ts/postgreslet/api/v1"
)

// StatusReconciler reconciles a Postgresql object
type StatusReconciler struct {
	SvcClient             client.Client
	CtrlClient            client.Client
	Log                   logr.Logger
	Scheme                *runtime.Scheme
	PartitionID           string
	ControlPlaneNamespace string
	EnableForceSharedIP   bool
}

// Reconcile updates the status of the remote Postgres object based on the status of the local zalando object.
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls/status,verbs=get;update;patch
func (r *StatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("postgresql", req.NamespacedName)

	log.Info("fetching postgresql")
	instance := &zalando.Postgresql{}
	if err := r.SvcClient.Get(ctx, req.NamespacedName, instance); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("status changed to Deleted")
		return ctrl.Result{}, nil
	}

	log.Info("Got status update", "PostgresClusterStatus", instance.Status.PostgresClusterStatus)

	derivedOwnerName, err := deriveOwnerName(instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("fetching owner")
	ownerNs := types.NamespacedName{
		Namespace: r.ControlPlaneNamespace,
		Name:      derivedOwnerName,
	}
	owner := &pg.Postgres{}
	if err := r.CtrlClient.Get(ctx, ownerNs, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("could not find the owner")
	}

	log.Info("updating status")
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh copy of the owner object
		if err := r.CtrlClient.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, owner); err != nil {
			return err
		}
		// update the status of the remote object
		owner.Status.Description = instance.Status.PostgresClusterStatus
		// update the reference to the zalando instance in the remote object
		owner.Status.ChildName = instance.ObjectMeta.Name

		log.Info("Updating owner", "owner", owner.UID)
		if err := r.CtrlClient.Status().Update(ctx, owner); err != nil {
			log.Error(err, "failed to update owner object")
			return err
		}
		return nil
	})

	if retryErr != nil {
		return ctrl.Result{}, retryErr
	}

	log.Info("updating socket")
	if owner.Spec.DedicatedLoadBalancerIP == nil {
		// no dedicated load balancer configured, use the shared one
		shared := &corev1.Service{}
		if err := r.SvcClient.Get(ctx, *owner.ToSharedSvcLBNamespacedName(), shared); err == nil {
			// update IP and port
			owner.Status.Socket.IP = shared.Spec.LoadBalancerIP
			owner.Status.Socket.Port = shared.Spec.Ports[0].Port

			if err := r.CtrlClient.Status().Update(ctx, owner); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update postgres status socket: %w", err)
			}
			log.Info("postgres status socket updated")
		} else {
			// Todo: Handle errors other than `NotFound`
			log.Info("unable to fetch the shared LoadBalancer to update postgres status socket")
		}
	} else {
		// get dedicated
		dedicated := &corev1.Service{}
		derr := r.SvcClient.Get(ctx, *owner.ToDedicatedSvcLBNamespacedName(), dedicated)

		// get shared (if enabled)
		var shared *corev1.Service
		var serr error
		if r.EnableForceSharedIP {
			shared = &corev1.Service{}
			serr = r.SvcClient.Get(ctx, *owner.ToSharedSvcLBNamespacedName(), shared)
			if serr != nil {
				log.Info("failed to fetch shared loadbalancer")
			}
		}

		if derr == nil && dedicated.Status.LoadBalancer.Ingress != nil && len(dedicated.Status.LoadBalancer.Ingress) == 1 {
			// the dedicated load balancer is usable, use it for status
			owner.Status.Socket.IP = dedicated.Spec.LoadBalancerIP
			owner.Status.Socket.Port = dedicated.Spec.Ports[0].Port

			// if available, set shared loadbalancer as additional socket
			if serr == nil && r.EnableForceSharedIP {
				additionalSocket := pg.Socket{
					IP:   shared.Spec.LoadBalancerIP,
					Port: shared.Spec.Ports[0].Port,
				}
				owner.Status.AdditionalSockets = []pg.Socket{additionalSocket}
			}

		} else if r.EnableForceSharedIP && serr == nil {
			// we failed to fetch the dedicated load balancer or it was not ready yet: use the shared loadbalancer as fallback
			owner.Status.Socket.IP = shared.Spec.LoadBalancerIP
			owner.Status.Socket.Port = shared.Spec.Ports[0].Port
		} else {
			// neither the dedicated nor the shared loadbalancer are available: use empty socket
			owner.Status.Socket = pg.Socket{}
		}

		// actually perform the status update
		if err := r.CtrlClient.Status().Update(ctx, owner); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update postgres status socket: %w", err)
		}
		log.Info("postgres status socket updated")

	}

	// TODO also update the port/ip of databases mentioned in owner.Spec.PostgresConnectionInfo so that e.g. CWNP are always up to date

	// Fetch the list of operator-generated secrets
	secrets := &corev1.SecretList{}
	if err := r.SvcClient.List(ctx, secrets, owner.ToUserPasswordsSecretListOption()...); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to fetch the list of operator generated secrets: %w", err)
	}

	if len(secrets.Items) == 0 {
		return ctrl.Result{Requeue: true}, nil
	}

	// TODO: #176 delete the secrets in the end as well

	if err := r.createOrUpdateSecret(ctx, owner, secrets, log); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("status reconciled")

	return ctrl.Result{}, nil
}

// SetupWithManager registers this controller for reconciliation of Postgresql resources
func (r *StatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// todo: Better the watch mechanishm on secrets that weren't generated by this controller

	l := map[string]string{
		pg.PartitionIDLabelName: r.PartitionID,
	}
	s := metav1.LabelSelector{
		MatchLabels: l,
	}
	lsp, err := predicate.LabelSelectorPredicate(s)
	if err != nil {
		return fmt.Errorf("failed to create LabelSelectorPredicate: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&zalando.Postgresql{}).
		WithEventFilter(lsp).
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
	result, err := controllerutil.CreateOrUpdate(ctx, r.CtrlClient, fetched, func() error {
		fetched.Data = secret.Data
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update the secret containing user password pairs: %w", err)
	}
	// todo: better the log
	log.Info("secret created or updated", "operation result", result)

	return nil
}

// Extract the UID of the owner object by reading the value of a certain label
func deriveOwnerName(instance *zalando.Postgresql) (string, error) {
	value, ok := instance.ObjectMeta.Labels[pg.NameLabelName]
	if !ok {
		return "", fmt.Errorf("could not derive owner reference")
	}
	return value, nil
}
