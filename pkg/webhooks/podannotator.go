package webhooks

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=mpod.kb.io

type PodAnnotator struct {
	Log logr.Logger
}

func (a *PodAnnotator) Default(ctx context.Context, obj runtime.Object) error {
	log := a.Log.WithValues("obj", obj)
	log.V(1).Info("handling admission request")

	pod, ok := obj.(*corev1.Pod)
	if !ok {
		log.V(1).Info("failed to cast object to pod")
		return fmt.Errorf("expected a Pod but got a %T", obj)
	}

	// mutate the fields in pod
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
		log.V(1).Info("Mutating Pod", "pod", pod)
	}
	pod.Annotations["example-mutating-admission-webhook"] = "foo"

	log.V(1).Info("done")
	return nil
}
