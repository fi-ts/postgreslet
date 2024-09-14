package webhooks

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:webhook:path=/mutate-apps-v1-statefulset,mutating=true,failurePolicy=fail,groups="apps",resources=statefulsets,verbs=create;update,versions=v1,name=sts.postgres.fits.cloud

type STSAnnotator struct {
	Log logr.Logger
}

func (a *STSAnnotator) Default(ctx context.Context, obj runtime.Object) error {
	log := a.Log.WithValues("obj", obj)
	log.V(1).Info("handling admission request")

	sts, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		log.V(1).Info("failed to cast object to statefulset")
		return fmt.Errorf("expected a Pod but got a %T", obj)
	}

	// mutate the fields in pod
	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	log.V(1).Info("Mutating StatefulSet", "sts", sts)
	sts.Annotations["example-mutating-admission-webhook"] = "sts"

	log.V(1).Info("done")
	return nil
}
