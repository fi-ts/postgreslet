package webhooks

import (
	"context"
	"encoding/json"
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate--v1-sts,mutating=true,failurePolicy=fail,groups="",resources=statefulsets,verbs=create;update,versions=v1

// stsAnnotator annotates StatefulSets
type FsGroupChangePolicySetter struct {
	SvcClient client.Client
	decoder   *admission.Decoder
}

func (a *FsGroupChangePolicySetter) Handle(ctx context.Context, req admission.Request) admission.Response {
	sts := &appsv1.StatefulSet{}
	err := a.decoder.Decode(req, sts)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// when the fsGroup field is set, also set the fsGroupChangePolicy to OnRootMismatch
	if sts != nil && sts.Spec.Template.Spec.SecurityContext != nil && sts.Spec.Template.Spec.SecurityContext.FSGroup != nil {
		*sts.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy = v1.FSGroupChangeOnRootMismatch
	}

	marshaledSts, err := json.Marshal(sts)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledSts)
}
