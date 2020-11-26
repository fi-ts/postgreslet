package manifest

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	crdScheme = runtime.NewScheme()
)

// init is required to correctly initialize the crdScheme package variable.
func init() {
	_ = apiextensionsv1.AddToScheme(crdScheme)
	// FIXME v1beta1 can be removed once we moved to controller-gen v0.4.0
	_ = apiextensionsv1beta1.AddToScheme(crdScheme)
}

func runtimeManifestListToUnstructured(l []runtime.Object) []*unstructured.Unstructured {
	res := []*unstructured.Unstructured{}
	for _, obj := range l {
		u := &unstructured.Unstructured{}
		if err := crdScheme.Convert(obj, u, nil); err != nil {
			log.Error(err, "error converting to unstructured object", "object-kind", obj.GetObjectKind())
			continue
		}
		res = append(res, u)
	}
	return res
}
