package webhooks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
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

	sts := &appsv1.StatefulSet{}
	err := a.Decoder.Decode(req, sts)
	if err != nil {
		log.Error(err, "failed to decode request")
		return admission.Errored(http.StatusBadRequest, err)
	}

	// when the fsGroup field is set, also set the fsGroupChangePolicy to OnRootMismatch
	if sts != nil && sts.Spec.Template.Spec.SecurityContext != nil && sts.Spec.Template.Spec.SecurityContext.FSGroup != nil {
		*sts.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy = v1.FSGroupChangeOnRootMismatch
		log.V(1).Info("Mutating StatefulSet", "sts", sts)
	}

	marshaledSts, err := json.Marshal(sts)
	if err != nil {
		log.Error(err, "failed to marshal response")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	log.V(1).Info("done")
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledSts)
}
