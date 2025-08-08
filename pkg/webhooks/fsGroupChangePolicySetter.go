package webhooks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=ignore,groups=core,resources=pods,verbs=create;update,versions=v1,name=fsgroupchangepolicy.postgres.fits.cloud

// FsGroupChangePolicySetter Adds securityContext.fsGroupChangePolicy=OnRootMismatch when the securityContext.fsGroup field is set
type FsGroupChangePolicySetter struct {
	SvcClient client.Client
	Decoder   admission.Decoder
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

	//
	// PodAntiAffinity
	//
	paa := v1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
			{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: pod.ObjectMeta.Labels,
				},
				TopologyKey: "kubernetes.io/hostname",
			},
		},
		PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
			{
				PodAffinityTerm: v1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: pod.ObjectMeta.Labels,
					},
					TopologyKey: "machine.metal-stack.io/rack",
				},
				Weight: 1,
			},
		},
	}
	// initialize if necessary
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &v1.Affinity{}
	}
	// force our podantiaffinity
	pod.Spec.Affinity.PodAntiAffinity = &paa

	marshaledSts, err := json.Marshal(pod)
	if err != nil {
		log.Error(err, "failed to marshal response")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	log.V(1).Info("done")
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledSts)
}
