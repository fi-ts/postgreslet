package webhooks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=mpod.kb.io

type PodAnnotator struct {
	Client  client.Client
	Decoder *admission.Decoder
	Log     logr.Logger
}

func (a *PodAnnotator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := a.Log.WithValues("name", req.Name, "ns", req.Namespace)
	log.V(1).Info("handleing admission request")

	pod := &corev1.Pod{}
	err := a.Decoder.Decode(req, pod)
	if err != nil {
		log.Error(err, "failed to decode request")
		return admission.Errored(http.StatusBadRequest, err)
	}

	// mutate the fields in pod
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
		log.V(1).Info("Mutating Pod", "pod", pod)
	}
	pod.Annotations["example-mutating-admission-webhook"] = "foo"

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		log.Error(err, "failed to marshal response")
		return admission.Errored(http.StatusInternalServerError, err)
	}
	log.V(1).Info("done")
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}
