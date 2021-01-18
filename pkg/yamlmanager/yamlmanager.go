package yamlmanager

import (
	"context"
	"io/ioutil"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type YAMLManager struct {
	meta.MetadataAccessor
	client.Client
	*runtime.Scheme
	yaml []byte
}

func New(client client.Client, fileName string, scheme *runtime.Scheme) (*YAMLManager, error) {
	bb, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	return &YAMLManager{
		MetadataAccessor: meta.NewAccessor(),
		Client:           client,
		Scheme:           scheme,
		yaml:             bb,
	}, nil
}

// todo: refactor
// todo: Add logger to exported functions.
func (y *YAMLManager) InstallYAML(ctx context.Context, namespace, s3BucketURL string) (objs []runtime.Object, err error) {
	// Make sure the namespace exists.
	objs, err = y.ensureNamespace(ctx, namespace, objs)
	if err != nil {
		return
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(y.Scheme).UniversalDeserializer()
	list := &corev1.List{}
	if _, _, err = deserializer.Decode(y.yaml, nil, list); err != nil {
		return
	}

	// Decode each YAML to `runtime.Object`, add the namespace to it and install it.
	for _, item := range list.Items {
		obj, _, er := deserializer.Decode(item.Raw, nil, nil)
		if er != nil {
			return objs, er
		}

		if err = y.ensureCleanMetadata(obj); err != nil {
			return
		}

		if err = y.SetNamespace(obj, namespace); err != nil {
			return
		}

		key, er := y.toObjectKey(obj, namespace)
		if er != nil {
			return objs, er
		}
		switch v := obj.(type) {
		case *v1.ServiceAccount:
			err = y.Get(ctx, key, &v1.ServiceAccount{})
		case *rbacv1.ClusterRole:
			key.Namespace = ""
			err = y.Get(ctx, key, &rbacv1.ClusterRole{})
		case *rbacv1.ClusterRoleBinding:
			// Set the namespace of the ServiceAccount in the ClusterRoleBinding.
			for i, s := range v.Subjects {
				if s.Kind == "ServiceAccount" {
					v.Subjects[i].Namespace = namespace
				}
			}

			// ClusterRoleBinding is not namespaced.
			key.Namespace = ""

			// If a ClusterRoleBinding already exists, patch it.
			got := &rbacv1.ClusterRoleBinding{}
			err = y.Get(ctx, key, got)
			if err == nil {
				patch := client.MergeFrom(got.DeepCopy())
				got.Subjects = append(got.Subjects, v.Subjects[0])
				if err = y.Patch(ctx, got, patch); err != nil {
					return
				}
			}
		case *v1.ConfigMap:
			y.editConfigMap(v, namespace, s3BucketURL)
			err = y.Get(ctx, key, &v1.ConfigMap{})
		case *v1.Service:
			err = y.Get(ctx, key, &v1.Service{})
		case *appsv1.Deployment:
			err = y.Get(ctx, key, &appsv1.Deployment{})
		}
		if err != nil {
			if errors.IsNotFound(err) {
				if err = y.Create(ctx, obj); err != nil {
					return
				}
			}
		}
		objs = append(objs, obj)
	}

	if err = y.waitTillZalandoPostgresOperatorReady(ctx, time.Minute, time.Second); err != nil {
		return
	}

	return
}

func (y *YAMLManager) UninstallYAML(ctx context.Context, objs []runtime.Object) error {
	for _, obj := range objs {
		if err := y.Delete(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

func (y *YAMLManager) UninstallUnstructured(ctx context.Context, u *unstructured.Unstructured) error {
	if err := y.Delete(ctx, u); err != nil {
		return err
	}
	return nil
}

func (y *YAMLManager) editConfigMap(cm *v1.ConfigMap, namespace, s3BucketURL string) {
	cm.Data["logical_backup_s3_bucket"] = s3BucketURL
	cm.Data["watched_namespace"] = namespace
}

func (y *YAMLManager) ensureCleanMetadata(obj runtime.Object) error {
	// Remove annotations.
	if err := y.MetadataAccessor.SetAnnotations(obj, nil); err != nil {
		return err
	}

	// Remove resourceVersion.
	if err := y.MetadataAccessor.SetResourceVersion(obj, ""); err != nil {
		return err
	}

	// Remove uid.
	if err := y.MetadataAccessor.SetUID(obj, ""); err != nil {
		return err
	}

	return nil
}

func (y *YAMLManager) ensureNamespace(ctx context.Context, namespace string, objs []runtime.Object) ([]runtime.Object, error) {
	if err := y.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return nil, err
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = namespace
		if err := y.Create(ctx, nsObj); err != nil {
			return nil, err
		}

		// Append the created namespace to the list of the created `runtime.Object`s.
		objs = append(objs, nsObj)
	}

	return objs, nil
}

func (y *YAMLManager) toObjectKey(obj runtime.Object, namespace string) (client.ObjectKey, error) {
	name, err := y.MetadataAccessor.Name(obj)
	if err != nil {
		return client.ObjectKey{}, err
	}
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, nil
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
