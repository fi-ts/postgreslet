package yamlmanager

import (
	"context"
	"io/ioutil"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type YAMLManager struct {
	client.Client
	*runtime.Scheme
}

func NewYAMLManager(conf *rest.Config, scheme *runtime.Scheme) (*YAMLManager, error) {
	// Use no-cache client to avoid waiting for cashing.
	client, err := client.New(conf, client.Options{})
	if err != nil {
		return nil, err
	}
	return &YAMLManager{
		Client: client,
		Scheme: scheme,
	}, nil
}

func (y *YAMLManager) InstallYAML(fileName, namespace string) (objs []runtime.Object, err error) {
	bb, err := ioutil.ReadFile(fileName)
	if err != nil {
		return
	}

	ctx := context.Background()

	// Make sure the namespace exists.
	if err = y.Client.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return
		}

		// Create the namespace.
		ns := &corev1.Namespace{}
		ns.Name = namespace
		if err = y.Create(ctx, ns); err != nil {
			return
		}

		// Append the created namespace to the list of the created `runtime.Object`s.
		objs = append(objs, ns)
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(y.Scheme).UniversalDeserializer()
	list := &corev1.List{}
	if _, _, err = deserializer.Decode(bb, nil, list); err != nil {
		return
	}

	// Decode each YAML to `runtime.Object`, add the namespace to it and install it.
	accessor := meta.NewAccessor()
	for _, item := range list.Items {
		obj, _, er := deserializer.Decode(item.Raw, nil, nil)
		if er != nil {
			return objs, er
		}

		// Remove annotations.
		if err = accessor.SetAnnotations(obj, nil); err != nil {
			return
		}

		if err = y.setNamespace(obj, namespace, accessor); err != nil {
			return
		}

		if err = y.Create(ctx, obj); err != nil {
			return
		}
		objs = append(objs, obj)
	}

	if err = y.waitTillZalandoPostgresOperatorReady(ctx, time.Minute, time.Second); err != nil {
		return
	}

	return
}

func (y *YAMLManager) UninstallYAML(objs []runtime.Object) error {
	for _, obj := range objs {
		if err := y.Delete(context.Background(), obj); err != nil {
			return err
		}
	}
	return nil
}

func (*YAMLManager) setNamespace(obj runtime.Object, namespace string, accessor meta.MetadataAccessor) error {
	if err := accessor.SetNamespace(obj, namespace); err != nil {
		return err
	}

	// Add the namespace to the `ServiceAccount` in the `ClusterRoleBinding`
	if v, ok := obj.(*rbacv1.ClusterRoleBinding); ok {
		for i, s := range v.Subjects {
			if s.Kind == "ServiceAccount" {
				v.Subjects[i].Namespace = namespace
			}
		}
	}

	return nil
}

func (y *YAMLManager) waitTillZalandoPostgresOperatorReady(ctx context.Context, timeout time.Duration, period time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Wait till there's at least one `postgres-operator` pod with status `running`.
	if err := wait.Poll(period, timeout, func() (bool, error) {
		// Fetch the pods with the matching labels.
		pods := &corev1.PodList{}
		if err := y.List(ctx, pods, client.MatchingLabels{"name": "postgres-operator"}); err != nil {
			// `Not found` isn't an error.
			return false, client.IgnoreNotFound(err)
		}
		if len(pods.Items) == 0 {
			return false, nil
		}

		// Roll the list to examine the status.
		for _, pod := range pods.Items {
			newPod := &corev1.Pod{}
			if err := y.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, newPod); err != nil {
				return false, err
			}
			if newPod.Status.Phase == corev1.PodRunning {
				return true, nil
			}
		}

		// Nothing found. Poll after the period.
		return false, nil
	}); err != nil {
		return err
	}

	return nil
}
