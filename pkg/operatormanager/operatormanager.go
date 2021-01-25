package operatormanager

import (
	"context"
	errs "errors"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/go-logr/logr"
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

// operatorPodMatchingLabels is for listing operator pods
var operatorPodMatchingLabels = client.MatchingLabels{"name": "postgres-operator"}

// OperatorManager manages the operator
type OperatorManager struct {
	client.Client
	runtime.Decoder
	list *corev1.List
	Log  logr.Logger
	meta.MetadataAccessor
	*runtime.Scheme
}

// New creates a new `OperatorManager`
func New(client client.Client, fileName string, scheme *runtime.Scheme, log logr.Logger) (*OperatorManager, error) {
	bb, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, fmt.Errorf("error while reading operator yaml file: %v", err)
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	list := &corev1.List{}
	if _, _, err := deserializer.Decode(bb, nil, list); err != nil {
		return nil, fmt.Errorf("error while converting bytes to a list of yamls: %v", err)
	}

	log.Info("new `OperatorManager` created")
	return &OperatorManager{
		MetadataAccessor: meta.NewAccessor(),
		Client:           client,
		Decoder:          deserializer,
		list:             list,
		Scheme:           scheme,
		Log:              log,
	}, nil
}

// InstallOperator installs the operator Stored in `OperatorManager`
func (m *OperatorManager) InstallOperator(ctx context.Context, namespace, s3BucketURL string) ([]runtime.Object, error) {
	objs := []runtime.Object{}

	// Make sure the namespace exists.
	objs, err := m.ensureNamespace(ctx, namespace, objs)
	if err != nil {
		return objs, fmt.Errorf("error while ensuring the existence of namespace %v: %v", namespace, err)
	}

	// Decode each YAML to `runtime.Object`, add the namespace to it and install it.
	for _, item := range m.list.Items {
		obj, _, err := m.Decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return objs, fmt.Errorf("error while converting yaml to `runtime.Object`: %v", err)
		}

		if objs, err := m.createNewRuntimeObject(ctx, objs, obj, namespace, s3BucketURL); err != nil {
			return objs, fmt.Errorf("error while creating the `runtime.Object`: %v", err)
		}
	}

	if err = m.waitTillOperatorReady(ctx, time.Minute, time.Second); err != nil {
		return objs, fmt.Errorf("error while waiting for the readiness of the operator: %v", err)
	}

	m.Log.Info("operator installed")
	return objs, nil
}

// IsOperatorDeletable returns true when there's no running instance operated by the operator
func (m *OperatorManager) IsOperatorDeletable(ctx context.Context, namespace string) (bool, error) {
	pods := &corev1.PodList{}
	if err := m.List(ctx, pods, client.InNamespace(namespace), m.toInstanceMatchingLabels(namespace)); err != nil {
		if errors.IsNotFound(err) {
			m.Log.Info("operator is deletable")
			return true, nil
		}
		return false, fmt.Errorf("error while fetching the list of instances operated by the operator: %v", err)
	}
	if len(pods.Items) == 0 {
		m.Log.Info("operator is deletable")
		return true, nil
	}
	return false, nil
}

// IsOperatorInstalled returns true when the operator is installed
func (m *OperatorManager) IsOperatorInstalled(ctx context.Context, namespace string) (bool, error) {
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		operatorPodMatchingLabels,
	}
	if err := m.List(ctx, pods, opts...); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if len(pods.Items) == 0 {
		return false, nil
	}
	m.Log.Info("operator is installed")
	return true, nil
}

// UninstallOperator uninstalls the operator
func (m *OperatorManager) UninstallOperator(ctx context.Context, namespace string) error {
	items := m.list.Items
	for i := range items {
		item := items[len(items)-1-i]
		obj, _, err := m.Decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return fmt.Errorf("error while converting yaml to `runtime.Onject`: %v", err)
		}

		if err := m.SetNamespace(obj, namespace); err != nil {
			return fmt.Errorf("error while setting the namesapce: %v", err)
		}

		switch v := obj.(type) {
		case *rbacv1.ClusterRole: // no-op
		case *rbacv1.ClusterRoleBinding:
			// Remove the ServiceAccount from ClusterRoleBinding's Subjects and then patch it.
			for i, s := range v.Subjects {
				if s.Kind == "ServiceAccount" && s.Namespace == namespace {
					patch := client.MergeFrom(v.DeepCopy())
					v.Subjects = append(v.Subjects[:i], v.Subjects[i+1:]...)
					if err = m.Patch(ctx, v, patch); err != nil {
						return fmt.Errorf("error while patching %v: %v", v, err)
					}
				}
			}
		default:
			if err := m.Delete(ctx, v); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
				return fmt.Errorf("error while deleting %v: %v", v, err)
			}
		}
	}

	m.Log.Info("operator deleted")
	return nil
}

// createNewRuntimeObject adds namespace to obj and creates or patches it
func (m *OperatorManager) createNewRuntimeObject(ctx context.Context, objs []runtime.Object, obj runtime.Object, namespace, s3BucketURL string) ([]runtime.Object, error) {
	if err := m.ensureCleanMetadata(obj); err != nil {
		return objs, fmt.Errorf("error while ensuring the metadata of the `runtime.Object` is clean: %v", err)
	}

	if err := m.SetNamespace(obj, namespace); err != nil {
		return objs, fmt.Errorf("error while setting the namespace of the `runtime.Object` to %v: %v", namespace, err)
	}

	key, err := m.toObjectKey(obj, namespace)
	if err != nil {
		return objs, fmt.Errorf("error while making the object key: %v", err)
	}
	switch v := obj.(type) {
	case *v1.ServiceAccount:
		m.Log.Info("handling ServiceAccount")
		err = m.Get(ctx, key, &v1.ServiceAccount{})
	case *rbacv1.ClusterRole:
		m.Log.Info("handling ClusterRole")
		// ClusterRole is not namespaced.
		key.Namespace = ""
		err = m.Get(ctx, key, &rbacv1.ClusterRole{})
	case *rbacv1.ClusterRoleBinding:
		m.Log.Info("handling ClusterRoleBinding")
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
			if err := m.Patch(ctx, got, patch); err != nil {
				return objs, fmt.Errorf("error while patching the `ClusterRoleBinding`: %v", err)
			}
			m.Log.Info("ClusterRoleBinding patched")
		}
	case *v1.ConfigMap:
		m.Log.Info("handling ConfigMap")
		m.editConfigMap(v, namespace, s3BucketURL)
		err = m.Get(ctx, key, &v1.ConfigMap{})
	case *v1.Service:
		m.Log.Info("handling Service")
		err = m.Get(ctx, key, &v1.Service{})
	case *appsv1.Deployment:
		m.Log.Info("handling Deployment")
		err = m.Get(ctx, key, &appsv1.Deployment{})
	default:
		return objs, errs.New("unknown `runtime.Object`")
	}
	if err != nil {
		if errors.IsNotFound(err) {
			if err := m.Create(ctx, obj); err != nil {
				return objs, fmt.Errorf("error while creating the `runtime.Object`: %v", err)
			}
			m.Log.Info("new `runtime.Object` created")

			// Append the newly created obj.
			objs = append(objs, obj)
			return objs, nil
		}
		return objs, fmt.Errorf("error while fetching the `runtime.Object`: %v", err)
	}

	return objs, nil
}

// editConfigMap adds info to cm
func (m *OperatorManager) editConfigMap(cm *v1.ConfigMap, namespace, s3BucketURL string) {
	cm.Data["logical_backup_s3_bucket"] = s3BucketURL
	cm.Data["watched_namespace"] = namespace
}

// ensureCleanMetadata ensures obj has clean metadata
func (m *OperatorManager) ensureCleanMetadata(obj runtime.Object) error {
	// Remove annotations.
	if err := m.MetadataAccessor.SetAnnotations(obj, nil); err != nil {
		return fmt.Errorf("error while removing annotations of the read k8s resource: %v", err)
	}

	// Remove resourceVersion.
	if err := m.MetadataAccessor.SetResourceVersion(obj, ""); err != nil {
		return fmt.Errorf("error while removing resourceVersion of the read k8s resource: %v", err)
	}

	// Remove uid.
	if err := m.MetadataAccessor.SetUID(obj, ""); err != nil {
		return fmt.Errorf("error while removing uid of the read k8s resource: %v", err)
	}

	return nil
}

// ensureNamespace ensures namespace exists
func (m *OperatorManager) ensureNamespace(ctx context.Context, namespace string, objs []runtime.Object) ([]runtime.Object, error) {
	if err := m.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("error while fetching namespace %v: %v", namespace, err)
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = namespace
		if err := m.Create(ctx, nsObj); err != nil {
			return nil, fmt.Errorf("error while creating namespace %v: %v", namespace, err)
		}

		// Append the created namespace to the list of the created `runtime.Object`s.
		objs = append(objs, nsObj)
	}

	return objs, nil
}

// toInstanceMatchingLabels makes the matching labels for the pods of the instances operated by the operator
func (m *OperatorManager) toInstanceMatchingLabels(namespace string) *client.MatchingLabels {
	return &client.MatchingLabels{"team": namespace, "application": "spilo"}
}

// toObjectKey makes ObjectKey from namespace and the name of obj
func (m *OperatorManager) toObjectKey(obj runtime.Object, namespace string) (client.ObjectKey, error) {
	name, err := m.MetadataAccessor.Name(obj)
	if err != nil {
		return client.ObjectKey{}, fmt.Errorf("error while extracting the name of the k8s resource: %v", err)
	}
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, nil
}

// waitTillOperatorReady waits till the operator pod is ready or timeout is reached, polling the status every period
func (m *OperatorManager) waitTillOperatorReady(ctx context.Context, timeout time.Duration, period time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Wait till there's at least one `postgres-operator` pod with status `running`.
	if err := wait.Poll(period, timeout, func() (bool, error) {
		// Fetch the pods with the matching labels.
		pods := &corev1.PodList{}
		if err := m.List(ctx, pods, operatorPodMatchingLabels); err != nil {
			// `Not found` isn't an error.
			return false, client.IgnoreNotFound(err)
		}
		if len(pods.Items) == 0 {
			return false, nil
		}

		// Roll the list to examine the status.
		for _, pod := range pods.Items {
			newPod := &corev1.Pod{}
			ns := pod.Namespace
			name := pod.Name
			if err := m.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, newPod); err != nil {
				return false, fmt.Errorf("error while fetching the operator pod with namespace %v and name %v: %v", ns, name, err)
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
