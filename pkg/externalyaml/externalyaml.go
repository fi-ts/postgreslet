package externalyaml

import (
	"context"
	"io/ioutil"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func InstallExternalYAML(fileName, namespace string, k8sClient client.Client, scheme *runtime.Scheme) (objs []runtime.Object, err error) {
	bb, err := ioutil.ReadFile(fileName)
	if err != nil {
		return
	}

	ctx := context.Background()

	// Make sure the namespace `partitionID` exists.
	if err = k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return
		}

		// Create the namespace.
		ns := &corev1.Namespace{}
		ns.Name = namespace
		if err = k8sClient.Create(ctx, ns); err != nil {
			return
		}

		// Append the created namespace to the list of the created `runtime.Object`s.
		objs = append(objs, ns)
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(scheme).UniversalDeserializer()
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

		if err = setNamespace(obj, namespace, accessor); err != nil {
			return
		}

		if err = k8sClient.Create(ctx, obj); err != nil {
			return
		}
		objs = append(objs, obj)
	}

	if err = waitTillZalandoPostgresOperatorReady(ctx, time.Minute, time.Second, k8sClient); err != nil {
		return
	}

	return
}

func UninstallExternalYaml(objs []runtime.Object, k8sClient client.Client) error {
	for _, obj := range objs {
		if err := k8sClient.Delete(context.Background(), obj); err != nil {
			return err
		}
	}
	return nil
}

func setNamespace(obj runtime.Object, namespace string, accessor meta.MetadataAccessor) error {
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
		log.Println("clusterrolebinding", v)
	}

	return nil
}

func waitTillZalandoPostgresOperatorReady(ctx context.Context, timeout time.Duration, period time.Duration, k8sClient client.Client) (err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Wait till there's at least one `postgres-operator` pod with status `running`.
	if err = wait.Poll(period, timeout, func() (bool, error) {
		// Fetch the pods with the matching labels.
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods, client.MatchingLabels{"name": "postgres-operator"}); err != nil {
			// `Not found` isn't an error.
			return false, client.IgnoreNotFound(err)
		}
		if len(pods.Items) == 0 {
			return false, nil
		}

		// Roll the list to examine the status.
		for _, pod := range pods.Items {
			newPod := &corev1.Pod{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, newPod); err != nil {
				return false, err
			}
			if newPod.Status.Phase == corev1.PodRunning {
				return true, nil
			}
		}

		// Nothing found. Poll after the period.
		return false, nil
	}); err != nil {
		return
	}

	return
}
