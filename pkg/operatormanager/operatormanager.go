/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package operatormanager

import (
	"context"
	"encoding/base64"
	errs "errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/go-logr/logr"
	coreosv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// The serviceAccount name to use for the database pods.
	// TODO: create new account per namespace
	// TODO: use different account for operator and database
	serviceAccountName string = "postgres-operator"

	// PodEnvCMName Name of the pod environment configmap to create and use
	PodEnvCMName string = "postgres-pod-config"

	operatorPodLabelName  string = "name"
	operatorPodLabelValue string = "postgres-operator"

	postgresExporterServiceName     string = "postgres-exporter"
	postgresExporterServicePortName string = "metrics"

	// SidecarsCMFluentBitConfKey Name of the key containing the fluent-bit.conf config file
	SidecarsCMFluentBitConfKey string = "fluent-bit.conf"
	// SidecarsCMExporterQueriesKey Name of the key containing the queries.yaml config file
	SidecarsCMExporterQueriesKey string = "queries.yaml"

	sidecarsCMName = "postgres-sidecars-configmap"
)

// operatorPodMatchingLabels is for listing operator pods
var operatorPodMatchingLabels = client.MatchingLabels{operatorPodLabelName: operatorPodLabelValue}

// Options
type Options struct {
	PspName       string
	OperatorImage string
	DockerImage   string
	EtcdHost      string
	CRDValidation bool
}

// OperatorManager manages the operator
type OperatorManager struct {
	client.Client
	runtime.Decoder
	list *corev1.List
	log  logr.Logger
	meta.MetadataAccessor
	*runtime.Scheme
	options Options
}

// New creates a new `OperatorManager`
func New(confRest *rest.Config, fileName string, scheme *runtime.Scheme, log logr.Logger, opt Options) (*OperatorManager, error) {
	// Use no-cache client to avoid waiting for cashing.
	client, err := client.New(confRest, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("error while creating new k8s client: %w", err)
	}

	bb, err := os.ReadFile(fileName)
	if err != nil {
		return nil, fmt.Errorf("error while reading operator yaml file: %w", err)
	}

	// Convert to a list of YAMLs.
	deserializer := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	list := &corev1.List{}
	if _, _, err := deserializer.Decode(bb, nil, list); err != nil {
		return nil, fmt.Errorf("error while converting bytes to a list of yamls: %w", err)
	}

	log.Info("new `OperatorManager` created")
	return &OperatorManager{
		MetadataAccessor: meta.NewAccessor(),
		Client:           client,
		Decoder:          deserializer,
		list:             list,
		Scheme:           scheme,
		log:              log,
		options:          opt,
	}, nil
}

// InstallOrUpdateOperator installs or updates the operator Stored in `OperatorManager`
func (m *OperatorManager) InstallOrUpdateOperator(ctx context.Context, namespace string) error {
	// Make sure the namespace exists.
	if err := m.createNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("error while ensuring the existence of namespace %v: %w", namespace, err)
	}

	// Add our (initially empty) custom pod environment configmap
	if err := m.createPodEnvironmentConfigMap(ctx, namespace); err != nil {
		return fmt.Errorf("error while creating pod environment configmap %v: %w", namespace, err)
	}

	// Add our sidecars configmap
	if err := m.createOrUpdateSidecarsConfig(ctx, namespace); err != nil {
		return fmt.Errorf("error while creating sidecars config %v: %w", namespace, err)
	}

	// Decode each YAML to `client.Object`, add the namespace to it and install it.
	for _, item := range m.list.Items {
		obj, _, err := m.Decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return fmt.Errorf("error while converting yaml to `client.Object`: %w", err)
		}

		cltObject, ok := obj.(client.Object)
		if !ok {
			return fmt.Errorf("unable to cast into client.Object")
		}
		if err := m.createNewClientObject(ctx, cltObject, namespace); err != nil {
			return fmt.Errorf("error while creating the `client.Object`: %w", err)
		}
	}

	m.log.Info("operator installed")
	return nil
}

// IsOperatorDeletable returns true when there's no running instance operated by the operator
func (m *OperatorManager) IsOperatorDeletable(ctx context.Context, namespace string) (bool, error) {
	setList := &appsv1.StatefulSetList{}
	if err := m.List(ctx, setList, client.InNamespace(namespace), m.toInstanceMatchingLabels()); client.IgnoreNotFound(err) != nil {
		return false, fmt.Errorf("error while fetching the list of statefulsets operated by the operator: %w", err)
	}
	if setList != nil && len(setList.Items) != 0 {
		m.log.Info("statefulset still running")
		return false, nil
	}

	services := &corev1.ServiceList{}
	if err := m.List(ctx, services, client.InNamespace(namespace), m.toInstanceMatchingLabels()); client.IgnoreNotFound(err) != nil {
		return false, fmt.Errorf("error while fetching the list of services operated by the operator: %w", err)
	}
	if services != nil && len(services.Items) != 0 {
		m.log.Info("services still running")
		return false, nil
	}

	m.log.Info("operator deletable")
	return true, nil
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
	m.log.Info("operator is installed")
	return true, nil
}

// UninstallOperator uninstalls the operator
func (m *OperatorManager) UninstallOperator(ctx context.Context, namespace string) error {
	items := m.list.Items
	for i := range items {
		item := items[len(items)-1-i]
		obj, _, err := m.Decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return fmt.Errorf("error while converting yaml to `runtime.Onject`: %w", err)
		}

		switch v := obj.(type) {
		case *rbacv1.ClusterRole: // no-op
		case *rbacv1.ClusterRoleBinding:
			// Fetch the ClusterRoleBinding
			objKey := types.NamespacedName{
				Namespace: v.Namespace,
				Name:      v.Name,
			}
			if err := m.Get(ctx, objKey, v); err != nil {
				return fmt.Errorf("error while fetching %v: %w", v, err)
			}

			// Remove the ServiceAccount from the ClusterRoleBinding's `Subjects` and then patch the ClusterRoleBinding
			for i, s := range v.Subjects {
				if s.Kind == "ServiceAccount" && s.Namespace == namespace {
					mergeFrom := client.MergeFrom(v.DeepCopy())
					v.Subjects = append(v.Subjects[:i], v.Subjects[i+1:]...)
					if err = m.Patch(ctx, v, mergeFrom); err != nil {
						return fmt.Errorf("error while patching %v: %w", v, err)
					}
				}
			}
		default:
			if err := m.SetNamespace(obj, namespace); err != nil {
				return fmt.Errorf("error while setting the namesapce: %w", err)
			}

			cltObject, ok := v.(client.Object)
			if !ok {
				return fmt.Errorf("unable to cast into client.Object")
			}
			if err := m.Delete(ctx, cltObject); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
				return fmt.Errorf("error while deleting %v: %w", v, err)
			}
		}
	}

	// delete the pod environment configmap
	if err := m.deletePodEnvironmentConfigMap(ctx, namespace); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("error while deleting pod environment configmap: %w", err)
	}

	// delete the postgres-exporter service
	if err := m.deleteExporterSidecarService(ctx, namespace); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
	}

	// Delete the namespace.
	nsObj := &corev1.Namespace{}
	nsObj.Name = namespace
	if err := m.Delete(ctx, nsObj); err != nil {
		return fmt.Errorf("error while deleting namespace %v: %w", namespace, err)
	}
	m.log.Info("namespace deleted")

	m.log.Info("operator and related ressources deleted")
	return nil
}

// createNewClientObject adds namespace to obj and creates or patches it
func (m *OperatorManager) createNewClientObject(ctx context.Context, obj client.Object, namespace string) error {
	// remove any unwanted annotations, uids etc. Remember, these objects come straight from the YAML.
	if err := m.ensureCleanMetadata(obj); err != nil {
		return fmt.Errorf("error while ensuring the metadata of the `client.Object` is clean: %w", err)
	}

	// use our current namespace, not the one from the YAML
	if err := m.SetNamespace(obj, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the `client.Object` to %v: %w", namespace, err)
	}

	// generate a proper object key for each object
	key, err := m.toObjectKey(obj, namespace)
	if err != nil {
		return fmt.Errorf("error while making the object key: %w", err)
	}

	// perform different modifications on the parsed objects based on their kind
	switch v := obj.(type) {
	case *corev1.ServiceAccount:
		m.log.Info("handling ServiceAccount")
		err = m.Get(ctx, key, &corev1.ServiceAccount{})
	case *rbacv1.ClusterRole:
		m.log.Info("handling ClusterRole")
		// Add our psp
		pspPolicyRule := rbacv1.PolicyRule{
			APIGroups:     []string{"extensions"},
			Verbs:         []string{"use"},
			Resources:     []string{"podsecuritypolicies"},
			ResourceNames: []string{m.options.PspName},
		}
		v.Rules = append(v.Rules, pspPolicyRule)

		// ClusterRole is not namespaced.
		key.Namespace = ""
		err = m.Get(ctx, key, &rbacv1.ClusterRole{})
	case *rbacv1.ClusterRoleBinding:
		m.log.Info("handling ClusterRoleBinding")

		// Set the namespace of the ServiceAccount in the ClusterRoleBinding.
		for i, s := range v.Subjects {
			if s.Kind == "ServiceAccount" {
				v.Subjects[i].Namespace = namespace
			}
		}

		// ClusterRoleBinding is not namespaced.
		key.Namespace = ""

		// Fetch the ClusterRoleBinding
		got := &rbacv1.ClusterRoleBinding{}
		if err := m.Get(ctx, key, got); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("error while fetching %v: %w", v, err)
			}

			// Create the ClusterRoleBinding
			if err := m.Create(ctx, v); err != nil {
				return fmt.Errorf("error while creating %v: %w", v, err)
			}
			m.log.Info("ClusterRoleBinding created")

			return nil
		}

		// If the ServiceAccount already exists, return.
		for i := range got.Subjects {
			if got.Subjects[i].Kind == "ServiceAccount" && got.Subjects[i].Namespace == namespace {
				return nil
			}
		}

		// Patch the already existing ClusterRoleBinding
		mergeFrom := client.MergeFrom(got.DeepCopy())
		got.Subjects = append(got.Subjects, v.Subjects[0])
		if err := m.Patch(ctx, got, mergeFrom); err != nil {
			return fmt.Errorf("error while patching the `ClusterRoleBinding`: %w", err)
		}
		m.log.Info("ClusterRoleBinding patched")

		// we already patched the object, no need to go through the update path at the bottom of this function
		return nil
	case *corev1.ConfigMap:
		m.log.Info("handling ConfigMap")
		m.editConfigMap(v, namespace, m.options)
		err = m.Get(ctx, key, &corev1.ConfigMap{})
	case *corev1.Service:
		m.log.Info("handling Service")
		got := corev1.Service{}
		err = m.Get(ctx, key, &got)
		if err == nil {
			// Copy the ResourceVersion
			v.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
			// Copy the ClusterIP
			v.Spec.ClusterIP = got.Spec.ClusterIP
		}
	case *appsv1.Deployment:
		m.log.Info("handling Deployment")
		if len(v.Spec.Template.Spec.Containers) != 1 {
			m.log.Info("Unexpected number of containers in deployment, ignoring.")
		} else if m.options.OperatorImage != "" {
			m.log.Info("Patching operator image", "image", m.options.OperatorImage)
			v.Spec.Template.Spec.Containers[0].Image = m.options.OperatorImage
		}
		err = m.Get(ctx, key, &appsv1.Deployment{})
	default:
		return errs.New("unknown `client.Object`")
	}

	if err != nil {
		if errors.IsNotFound(err) {
			// the object (with that objectKey) does not exist yet, so we create it
			if err := m.Create(ctx, obj); err != nil {
				return fmt.Errorf("error while creating the `client.Object`: %w", err)
			}
			m.log.Info("new `client.Object` created")

			return nil
		}
		// something else went horribly wrong, abort
		return fmt.Errorf("error while fetching the `client.Object`: %w", err)
	}

	// if we made it this far, the object already exists, so we just update it
	if err := m.Update(ctx, obj); err != nil {
		return fmt.Errorf("error while updating the `client.Object`: %w", err)
	}

	return nil
}

// editConfigMap adds info to cm
func (m *OperatorManager) editConfigMap(cm *corev1.ConfigMap, namespace string, options Options) {
	cm.Data["watched_namespace"] = namespace
	// TODO don't use the same serviceaccount for operator and databases, see #88
	cm.Data["pod_service_account_name"] = serviceAccountName
	// set the reference to our custom pod environment configmap
	cm.Data["pod_environment_configmap"] = PodEnvCMName
	// set the list of inherited labels that will be passed on to the pods
	s := []string{pg.TenantLabelName, pg.ProjectIDLabelName, pg.UIDLabelName, pg.NameLabelName}
	// TODO maybe use a precompiled string here
	cm.Data["inherited_labels"] = strings.Join(s, ",")

	if options.DockerImage != "" {
		cm.Data["docker_image"] = options.DockerImage
	}

	if options.EtcdHost != "" {
		cm.Data["etcd_host"] = options.EtcdHost
	}

	cm.Data["enable_crd_validation"] = strconv.FormatBool(options.CRDValidation)
}

// ensureCleanMetadata ensures obj has clean metadata
func (m *OperatorManager) ensureCleanMetadata(obj runtime.Object) error {
	// Remove annotations.
	if err := m.MetadataAccessor.SetAnnotations(obj, nil); err != nil {
		return fmt.Errorf("error while removing annotations of the read k8s resource: %w", err)
	}

	// Remove resourceVersion.
	if err := m.MetadataAccessor.SetResourceVersion(obj, ""); err != nil {
		return fmt.Errorf("error while removing resourceVersion of the read k8s resource: %w", err)
	}

	// Remove uid.
	if err := m.MetadataAccessor.SetUID(obj, ""); err != nil {
		return fmt.Errorf("error while removing uid of the read k8s resource: %w", err)
	}

	return nil
}

// createNamespace ensures namespace exists
func (m *OperatorManager) createNamespace(ctx context.Context, namespace string) error {
	if err := m.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return fmt.Errorf("error while fetching namespace %v: %w", namespace, err)
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = namespace
		nsObj.ObjectMeta.Labels = map[string]string{
			pg.ManagedByLabelName: pg.ManagedByLabelValue,
		}
		if err := m.Create(ctx, nsObj); err != nil {
			return fmt.Errorf("error while creating namespace %v: %w", namespace, err)
		}
		m.log.Info("namespace created", "name", namespace)
	}

	return nil
}

// createPodEnvironmentConfigMap creates a new ConfigMap with additional environment variables for the pods
func (m *OperatorManager) createPodEnvironmentConfigMap(ctx context.Context, namespace string) error {
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      PodEnvCMName,
	}
	if err := m.Get(ctx, ns, &corev1.ConfigMap{}); err == nil {
		// configmap already exists, nothing to do here
		// we will update the configmap with the correct S3 config in the postgres controller
		m.log.Info("Pod Environment ConfigMap already exists")
		return nil
	}

	cm := &corev1.ConfigMap{}
	if err := m.SetName(cm, PodEnvCMName); err != nil {
		return fmt.Errorf("error while setting the name of the new Pod Environment ConfigMap to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(cm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the new Pod Environment ConfigMap to %v: %w", namespace, err)
	}

	if err := m.Create(ctx, cm); err != nil {
		return fmt.Errorf("error while creating the new Pod Environment ConfigMap: %w", err)
	}
	m.log.Info("new Pod Environment ConfigMap created")

	return nil
}

func (m *OperatorManager) createOrUpdateSidecarsConfig(ctx context.Context, namespace string) error {
	// try to fetch the global sidecars configmap
	cns := types.NamespacedName{
		// TODO don't use string literals here! name is dependent of the release name of the helm chart!
		Namespace: "postgreslet-system",
		Name:      "postgreslet-postgres-sidecars",
	}
	c := &corev1.ConfigMap{}
	if err := m.Get(ctx, cns, c); err != nil {
		// configmap with configuration does not exists, nothing we can do here...
		m.log.Error(err, "could not fetch config for sidecars")
		return err
	}

	// Add our sidecars configmap
	if err := m.createOrUpdateSidecarsConfigMap(ctx, namespace, c); err != nil {
		return fmt.Errorf("error while creating sidecars configmap %v: %w", namespace, err)
	}

	// Add services for our sidecars
	if err := m.createOrUpdateExporterSidecarService(ctx, namespace, c); err != nil {
		return fmt.Errorf("error while creating sidecars services %v: %w", namespace, err)
	}

	// Add services for our sidecars
	err := m.createOrUpdateExporterSidecarServiceMonitor(ctx, namespace)
	if err != nil {
		return fmt.Errorf("error while creating sidecars servicemonitor %v: %w", namespace, err)
	}

	return nil
}

func (m *OperatorManager) createOrUpdateSidecarsConfigMap(ctx context.Context, namespace string, c *corev1.ConfigMap) error {
	sccm := &corev1.ConfigMap{}
	if err := m.SetName(sccm, sidecarsCMName); err != nil {
		return fmt.Errorf("error while setting the name of the new Sidecars ConfigMap to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(sccm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the new Sidecars ConfigMap to %v: %w", namespace, err)
	}

	// initialize map
	sccm.Data = make(map[string]string)

	// decode and write the fluentd.conf key from the global configmap to the local configmap
	b, err := base64.StdEncoding.DecodeString(c.Data["fluent-bit.conf"])
	if err == nil {

		sccm.Data[SidecarsCMFluentBitConfKey] = string(b)
	}

	// decode and write the queries.yaml key from the global configmap to the local configmap
	b, err = base64.StdEncoding.DecodeString(c.Data["queries.yaml"])
	if err == nil {
		sccm.Data[SidecarsCMExporterQueriesKey] = string(b)
	}

	// try to fetch any existing local sidecars configmap
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      sidecarsCMName,
	}
	if err := m.Get(ctx, ns, &corev1.ConfigMap{}); err == nil {
		// local configmap aleady exists, updating it
		if err := m.Update(ctx, sccm); err != nil {
			return fmt.Errorf("error while updating the new Sidecars ConfigMap: %w", err)
		}
		m.log.Info("Sidecars ConfigMap updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local configmap does not exist, creating it
	if err := m.Create(ctx, sccm); err != nil {
		return fmt.Errorf("error while creating the new Sidecars ConfigMap: %w", err)
	}
	m.log.Info("new Sidecars ConfigMap created")

	return nil
}

// createOrUpdateExporterSidecarService ensures the neccessary services to acces the sidecars exist
func (m *OperatorManager) createOrUpdateExporterSidecarService(ctx context.Context, namespace string, c *corev1.ConfigMap) error {
	exporterServicePort, error := strconv.ParseInt(c.Data["postgres-exporter-service-port"], 10, 32)
	if error != nil {
		// todo log error
		exporterServicePort = 9187
	}

	exporterServiceTargetPort, error := strconv.ParseInt(c.Data["postgres-exporter-service-target-port"], 10, 32)
	if error != nil {
		// todo log error
		exporterServiceTargetPort = exporterServicePort
	}

	pes := &corev1.Service{}

	if err := m.SetName(pes, postgresExporterServiceName); err != nil {
		return fmt.Errorf("error while setting the name of the postgres-exporter service to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(pes, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter service to %v: %w", namespace, err)
	}
	labels := map[string]string{
		// "application": "spilo", // TODO check if we still need that label, IsOperatorDeletable won't work anymore if we set it.
		"app": "postgres-exporter",
	}
	if err := m.SetLabels(pes, labels); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter service to %v: %w", namespace, err)
	}

	pes.Spec.Ports = []corev1.ServicePort{
		{
			Name:       postgresExporterServicePortName,
			Port:       int32(exporterServicePort),
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt(int(exporterServiceTargetPort)),
		},
	}
	selector := map[string]string{
		"application": "spilo",
	}
	pes.Spec.Selector = selector
	pes.Spec.Type = corev1.ServiceTypeClusterIP

	// try to fetch any existing postgres-exporter service
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      postgresExporterServiceName,
	}
	old := &corev1.Service{}
	if err := m.Get(ctx, ns, old); err == nil {
		// service exists, overwriting it (but using the same clusterip)
		pes.Spec.ClusterIP = old.Spec.ClusterIP
		pes.ObjectMeta.ResourceVersion = old.GetObjectMeta().GetResourceVersion()
		if err := m.Update(ctx, pes); err != nil {
			return fmt.Errorf("error while updating the postgres-exporter service: %w", err)
		}
		m.log.Info("postgres-exporter service updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := m.Create(ctx, pes); err != nil {
		return fmt.Errorf("error while creating the postgres-exporter service: %w", err)
	}
	m.log.Info("postgres-exporter service created")

	return nil
}

// createOrUpdateExporterSidecarService ensures the neccessary services to acces the sidecars exist
func (m *OperatorManager) createOrUpdateExporterSidecarServiceMonitor(ctx context.Context, namespace string) error {

	pesm := &coreosv1.ServiceMonitor{}

	// TODO what's the correct name?
	if err := m.SetName(pesm, postgresExporterServiceName); err != nil {
		return fmt.Errorf("error while setting the name of the postgres-exporter servicemonitor to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(pesm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter servicemonitor to %v: %w", namespace, err)
	}
	labels := map[string]string{
		// TODO add labels for tenant and project
		"app":     "postgres-exporter",
		"release": "prometheus",
	}
	if err := m.SetLabels(pesm, labels); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter servicemonitor to %v: %w", namespace, err)
	}

	pesm.Spec.Endpoints = []coreosv1.Endpoint{
		{
			Port: postgresExporterServicePortName,
		},
	}
	pesm.Spec.NamespaceSelector = coreosv1.NamespaceSelector{
		MatchNames: []string{namespace},
	}
	matchLabels := map[string]string{
		// TODO use extraced string
		"app": "postgres-exporter",
	}
	pesm.Spec.Selector = metav1.LabelSelector{
		MatchLabels: matchLabels,
	}

	// try to fetch any existing postgres-exporter service
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      postgresExporterServiceName,
	}
	old := &coreosv1.ServiceMonitor{}
	if err := m.Get(ctx, ns, old); err == nil {
		// Copy the resource version
		pesm.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
		if err := m.Update(ctx, pesm); err != nil {
			return fmt.Errorf("error while updating the postgres-exporter servicemonitor: %w", err)
		}
		m.log.Info("postgres-exporter servicemonitor updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local servicemonitor does not exist, creating it
	if err := m.Create(ctx, pesm); err != nil {
		return fmt.Errorf("error while creating the postgres-exporter servicemonitor: %w", err)
	}
	m.log.Info("postgres-exporter servicemonitor created")

	return nil
}

func (m *OperatorManager) deletePodEnvironmentConfigMap(ctx context.Context, namespace string) error {
	cm := &corev1.ConfigMap{}
	if err := m.SetName(cm, PodEnvCMName); err != nil {
		return fmt.Errorf("error while setting the name of the Pod Environment ConfigMap to delete to %v: %w", PodEnvCMName, err)
	}
	if err := m.SetNamespace(cm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the Pod Environment ConfigMap to delete to %v: %w", namespace, err)
	}
	if err := m.Delete(ctx, cm); err != nil {
		return fmt.Errorf("error while deleting the Pod Environment ConfigMap: %w", err)
	}
	m.log.Info("Pod Environment ConfigMap deleted")

	return nil
}

func (m *OperatorManager) deleteExporterSidecarService(ctx context.Context, namespace string) error {
	s := &corev1.Service{}
	if err := m.SetName(s, postgresExporterServiceName); err != nil {
		return fmt.Errorf("error while setting the name of the postgres-exporter service to delete to %v: %w", PodEnvCMName, err)
	}
	if err := m.SetNamespace(s, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the postgres-exporter service to delete to %v: %w", namespace, err)
	}
	if err := m.Delete(ctx, s); err != nil {
		return fmt.Errorf("error while deleting the postgres-exporter service: %w", err)
	}
	m.log.Info("postgres-exporter service deleted")

	return nil
}

// toInstanceMatchingLabels makes the matching labels for the pods of the instances operated by the operator
func (m *OperatorManager) toInstanceMatchingLabels() *client.MatchingLabels {
	return &client.MatchingLabels{"application": "spilo"}
}

// toObjectKey makes ObjectKey from namespace and the name of obj
func (m *OperatorManager) toObjectKey(obj runtime.Object, namespace string) (client.ObjectKey, error) {
	name, err := m.MetadataAccessor.Name(obj)
	if err != nil {
		return client.ObjectKey{}, fmt.Errorf("error while extracting the name of the k8s resource: %w", err)
	}
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, nil
}

// UpdateAllOperators Updates the manifests of all postgres operators managed by the postgreslet
func (m *OperatorManager) UpdateAllOperators(ctx context.Context) error {
	// fetch all operators (running or otherwise)
	m.log.Info("Fetching all managed namespaces")
	matchingLabels := client.MatchingLabels{
		pg.ManagedByLabelName: pg.ManagedByLabelValue,
	}
	nsList := &corev1.NamespaceList{}
	opts := []client.ListOption{
		matchingLabels,
	}
	if err := m.List(ctx, nsList, opts...); err != nil {
		return client.IgnoreNotFound(err)
	}
	// update each namespace
	for _, ns := range nsList.Items {
		m.log.Info("Updating postgres operator installation", "namespace", ns.Name)
		if err := m.InstallOrUpdateOperator(ctx, ns.Name); err != nil {
			return err
		}
	}

	m.log.Info("Done updating postgres operators in managed namespaces")
	return nil
}
