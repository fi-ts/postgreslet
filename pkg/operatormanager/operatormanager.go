package operatormanager

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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type OperatorManager struct {
	client.Client
	runtime.Decoder
	list *corev1.List
	meta.MetadataAccessor
	*runtime.Scheme
}

func New(client client.Client, fileName string, scheme *runtime.Scheme) (*OperatorManager, error) {
	bb, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	list := &corev1.List{}
	if _, _, err := deserializer.Decode(bb, nil, list); err != nil {
		return nil, err
	}

	return &OperatorManager{
		MetadataAccessor: meta.NewAccessor(),
		Client:           client,
		Decoder:          deserializer,
		list:             list,
		Scheme:           scheme,
	}, nil
}

// todo: refactor
// todo: Add logger to exported functions.
func (m *OperatorManager) InstallOperator(ctx context.Context, namespace, s3BucketURL string) (objs []runtime.Object, err error) {
	// Make sure the namespace exists.
	objs, err = m.ensureNamespace(ctx, namespace, objs)
	if err != nil {
		return
	}

	// Decode each YAML to `runtime.Object`, add the namespace to it and install it.
	for _, item := range m.list.Items {
		obj, _, er := m.Decoder.Decode(item.Raw, nil, nil)
		if er != nil {
			return objs, er
		}

		if objs, err = m.createRuntimeObject(ctx, objs, obj, namespace, s3BucketURL); err != nil {
			return
		}
	}

	if err = m.waitTillZalandoPostgresOperatorReady(ctx, time.Minute, time.Second); err != nil {
		return
	}

	return
}

// IsOperatorDeletable returns true when there's no running instance operated by the operator
func (m *OperatorManager) IsOperatorDeletable(ctx context.Context, namespace string) (bool, error) {
	pods := &corev1.PodList{}
	if err := m.List(ctx, pods, client.InNamespace(namespace), m.toInstanceMatchingLabels(namespace)); err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if len(pods.Items) == 0 {
		return true, nil
	}
	return false, nil
}

func (m *OperatorManager) IsOperatorInstalled(ctx context.Context, namespace string) (bool, error) {
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{"name": "postgres-operator"},
	}
	if err := m.List(ctx, pods, opts...); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if len(pods.Items) == 0 {
		return false, nil
	}
	return true, nil
}

func (m *OperatorManager) UninstallOperator(ctx context.Context, namespace string) error {
	items := m.list.Items
	for i := range items {
		item := items[len(items)-1-i]
		obj, _, err := m.Decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return err
		}

		if err := m.SetNamespace(obj, namespace); err != nil {
			return err
		}

		switch v := obj.(type) {
		case *rbacv1.ClusterRole:
		case *rbacv1.ClusterRoleBinding:
			// Remove the ServiceAccount away from ClusterRoleBinding's Subjects and then patch it.
			for i, s := range v.Subjects {
				if s.Kind == "ServiceAccount" && s.Namespace == namespace {
					patch := client.MergeFrom(v.DeepCopy())
					v.Subjects = append(v.Subjects[:i], v.Subjects[i+1:]...)
					if err = m.Patch(ctx, v, patch); err != nil {
						return err
					}
				}
			}
		default:
			if err := m.Delete(ctx, v); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
				return err
			}
		}
	}

	return nil
}

func (m *OperatorManager) createRuntimeObject(ctx context.Context, objs []runtime.Object, obj runtime.Object, namespace, s3BucketURL string) ([]runtime.Object, error) {
	if err := m.ensureCleanMetadata(obj); err != nil {
		return objs, err
	}

	if err := m.SetNamespace(obj, namespace); err != nil {
		return objs, err
	}

	key, err := m.toObjectKey(obj, namespace)
	if err != nil {
		return objs, err
	}
	switch v := obj.(type) {
	case *v1.ServiceAccount:
		err = m.Get(ctx, key, &v1.ServiceAccount{})
	case *rbacv1.ClusterRole:
		// ClusterRole is not namespaced.
		key.Namespace = ""
		err = m.Get(ctx, key, &rbacv1.ClusterRole{})
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
		err = m.Get(ctx, key, got)
		if err == nil {
			patch := client.MergeFrom(got.DeepCopy())
			got.Subjects = append(got.Subjects, v.Subjects[0])
			if err = m.Patch(ctx, got, patch); err != nil {
				return objs, err
			}
		}
	case *v1.ConfigMap:
		m.editConfigMap(v, namespace, s3BucketURL)
		err = m.Get(ctx, key, &v1.ConfigMap{})
	case *v1.Service:
		err = m.Get(ctx, key, &v1.Service{})
	case *appsv1.Deployment:
		err = m.Get(ctx, key, &appsv1.Deployment{})
	}
	if err != nil {
		if errors.IsNotFound(err) {
			if err = m.Create(ctx, obj); err != nil {
				return objs, err
			}

			// Append the newly created obj.
			objs = append(objs, obj)
		}
	}

	return objs, nil
}

func (m *OperatorManager) editConfigMap(cm *v1.ConfigMap, namespace, s3BucketURL string) {
	cm.Data["logical_backup_s3_bucket"] = s3BucketURL
	cm.Data["watched_namespace"] = namespace
}

func (m *OperatorManager) ensureCleanMetadata(obj runtime.Object) error {
	// Remove annotations.
	if err := m.MetadataAccessor.SetAnnotations(obj, nil); err != nil {
		return err
	}

	// Remove resourceVersion.
	if err := m.MetadataAccessor.SetResourceVersion(obj, ""); err != nil {
		return err
	}

	// Remove uid.
	if err := m.MetadataAccessor.SetUID(obj, ""); err != nil {
		return err
	}

	return nil
}

func (m *OperatorManager) ensureNamespace(ctx context.Context, namespace string, objs []runtime.Object) ([]runtime.Object, error) {
	if err := m.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return nil, err
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = namespace
		if err := m.Create(ctx, nsObj); err != nil {
			return nil, err
		}

		// Append the created namespace to the list of the created `runtime.Object`s.
		objs = append(objs, nsObj)
	}

	return objs, nil
}

func (m *OperatorManager) toInstanceMatchingLabels(namespace string) *client.MatchingLabels {
	return &client.MatchingLabels{"team": namespace, "application": "spilo"}
}

func (m *OperatorManager) toObjectKey(obj runtime.Object, namespace string) (client.ObjectKey, error) {
	name, err := m.MetadataAccessor.Name(obj)
	if err != nil {
		return client.ObjectKey{}, err
	}
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, nil
}

func (m *OperatorManager) waitTillZalandoPostgresOperatorReady(ctx context.Context, timeout time.Duration, period time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Wait till there's at least one `postgres-operator` pod with status `running`.
	if err := wait.Poll(period, timeout, func() (bool, error) {
		// Fetch the pods with the matching labels.
		pods := &corev1.PodList{}
		if err := m.List(ctx, pods, client.MatchingLabels{"name": "postgres-operator"}); err != nil {
			// `Not found` isn't an error.
			return false, client.IgnoreNotFound(err)
		}
		if len(pods.Items) == 0 {
			return false, nil
		}

		// Roll the list to examine the status.
		for _, pod := range pods.Items {
			newPod := &corev1.Pod{}
			if err := m.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, newPod); err != nil {
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
