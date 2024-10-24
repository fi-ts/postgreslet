package webhooks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-apps-v1-statefulset,mutating=true,failurePolicy=ignore,groups=apps,resources=statefulsets,verbs=create;update,versions=v1,name=fsgroupchangepolicy.postgres.fits.cloud

// FsGroupChangePolicySetter Adds securityContext.fsGroupChangePolicy=OnRootMismatch when the securityContext.fsGroup field is set
type FsGroupChangePolicySetter struct {
	SvcClient client.Client
	Decoder   *admission.Decoder
	Log       logr.Logger
}

func (a *FsGroupChangePolicySetter) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := a.Log.WithValues("name", req.Name, "ns", req.Namespace)
	log.V(1).Info("handling admission request")

	pod := &v1.Pod{}
	err := a.Decoder.Decode(req, pod)
	if err != nil {
		log.Error(err, "failed to decode request")
		return admission.Errored(http.StatusBadRequest, err)
	}

	// when the fsGroup field is set, also set the fsGroupChangePolicy to OnRootMismatch
	if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.FSGroup != nil {
		p := v1.FSGroupChangeOnRootMismatch
		pod.Spec.SecurityContext.FSGroupChangePolicy = &p
		log.V(1).Info("Mutating Pod", "pod", pod)
	}

	marshaledSts, err := json.Marshal(pod)
	if err != nil {
		log.Error(err, "failed to marshal response")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	log.V(1).Info("done")
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledSts)
}
