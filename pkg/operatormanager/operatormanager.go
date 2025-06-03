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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// The serviceAccount name to use for the database pods.
	serviceAccountName string = "postgres-pod"

	// PodEnvCMName Name of the pod environment configmap to create and use
	PodEnvCMName string = "postgres-pod-config"

	// PodEnvSecretName Name of the pod environment secret to create and use
	PodEnvSecretName string = "postgres-pod-secret" //nolint:gosec

	// SidecarsCMFluentBitConfKey Name of the key containing the fluent-bit.conf config file
	SidecarsCMFluentBitConfKey string = "fluent-bit.conf"
	// SidecarsCMExporterQueriesKey Name of the key containing the queries.yaml config file
	SidecarsCMExporterQueriesKey string = "queries.yaml"

	localSidecarsCMName = "postgres-sidecars-configmap"

	spilo_fsgroup     = "103"
	debugLogLevel int = 1
)

// Options
type Options struct {
	PspName                 string
	OperatorImage           string
	DockerImage             string
	EtcdHost                string
	CRDRegistration         bool
	MajorVersionUpgradeMode string
	PostgresletNamespace    string
	SidecarsConfigMapName   string
	PodAntiaffinity         bool
	PartitionID             string
	PatroniFailsafeMode     bool
}

// OperatorManager manages the operator
type OperatorManager struct {
	client           client.Client
	decoder          runtime.Decoder
	list             *corev1.List
	log              logr.Logger
	metadataAccessor meta.MetadataAccessor
	scheme           *runtime.Scheme
	options          Options
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
		metadataAccessor: meta.NewAccessor(),
		client:           client,
		decoder:          deserializer,
		list:             list,
		scheme:           scheme,
		log:              log.V(debugLogLevel),
		options:          opt,
	}, nil
}

// InstallOrUpdateOperator installs or updates the operator Stored in `OperatorManager`
func (m *OperatorManager) InstallOrUpdateOperator(ctx context.Context, namespace string) error {

	// Make sure the namespace exists.
	if err := m.createOrUpdateNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("error while ensuring the existence of namespace %v: %w", namespace, err)
	}

	// Add our (initially empty) custom pod environment configmap
	if _, err := m.CreatePodEnvironmentConfigMap(ctx, namespace); err != nil {
		return fmt.Errorf("error while creating pod environment configmap %v: %w", namespace, err)
	}

	// Add our (initially empty) custom pod environment secret
	if _, err := m.CreateOrGetPodEnvironmentSecret(ctx, namespace); err != nil {
		return fmt.Errorf("error while creating pod environment secret %v: %w", namespace, err)
	}

	// Add our sidecars configmap
	if err := m.createOrUpdateSidecarsConfig(ctx, namespace); err != nil {
		return fmt.Errorf("error while creating sidecars config %v: %w", namespace, err)
	}

	// Decode each YAML to `client.Object`, add the namespace to it and install it.
	for _, item := range m.list.Items {
		obj, _, err := m.decoder.Decode(item.Raw, nil, nil)
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

	m.log.Info("operator installed", "ns", namespace)
	return nil
}

// IsOperatorDeletable returns true when there's no running instance operated by the operator
func (m *OperatorManager) IsOperatorDeletable(ctx context.Context, namespace string, name string) (bool, error) {
	log := m.log.WithValues("ns", namespace)

	setList := &appsv1.StatefulSetList{}
	if err := m.client.List(ctx, setList, client.InNamespace(namespace), m.toInstanceMatchingLabels()); client.IgnoreNotFound(err) != nil {
		return false, fmt.Errorf("error while fetching the list of statefulsets operated by the operator: %w", err)
	}
	if len(setList.Items) != 0 {
		log.Info("statefulset still running")
		return false, nil
	}

	// Try fetch service
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	err := m.client.Get(ctx, ns, &corev1.Service{})
	if !errors.IsNotFound(err) {
		log.Info("service still running")
		return false, nil
	}

	log.Info("operator deletable")
	return true, nil
}

// UninstallOperator uninstalls the operator
func (m *OperatorManager) UninstallOperator(ctx context.Context, namespace string) error {
	items := m.list.Items
	for i := range items {
		item := items[len(items)-1-i]
		obj, _, err := m.decoder.Decode(item.Raw, nil, nil)
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
			if err := m.client.Get(ctx, objKey, v); err != nil {
				return fmt.Errorf("error while fetching %v: %w", v, err)
			}

			// Remove the ServiceAccount from the ClusterRoleBinding's `Subjects` and then patch the ClusterRoleBinding
			for i, s := range v.Subjects {
				if s.Kind == "ServiceAccount" && s.Namespace == namespace {
					mergeFrom := client.MergeFrom(v.DeepCopy())
					v.Subjects = append(v.Subjects[:i], v.Subjects[i+1:]...)
					if err = m.client.Patch(ctx, v, mergeFrom); err != nil {
						return fmt.Errorf("error while patching %v: %w", v, err)
					}
				}
			}
		default:
			if err := m.metadataAccessor.SetNamespace(obj, namespace); err != nil {
				return fmt.Errorf("error while setting the namespace: %w", err)
			}

			cltObject, ok := v.(client.Object)
			if !ok {
				return fmt.Errorf("unable to cast into client.Object")
			}
			if err := m.client.Delete(ctx, cltObject); err != nil {
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

	// Delete the namespace.
	nsObj := &corev1.Namespace{}
	nsObj.Name = namespace
	if err := m.client.Delete(ctx, nsObj); err != nil {
		return fmt.Errorf("error while deleting namespace %v: %w", namespace, err)
	}
	m.log.Info("namespace deleted", "ns", namespace)

	return nil
}

// createNewClientObject adds namespace to obj and creates or patches it
func (m *OperatorManager) createNewClientObject(ctx context.Context, obj client.Object, namespace string) error {
	log := m.log.WithValues("ns", namespace)

	// remove any unwanted annotations, uids etc. Remember, these objects come straight from the YAML.
	if err := m.ensureCleanMetadata(obj); err != nil {
		return fmt.Errorf("error while ensuring the metadata of the `client.Object` is clean: %w", err)
	}

	// use our current namespace, not the one from the YAML
	if err := m.metadataAccessor.SetNamespace(obj, namespace); err != nil {
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
		log.Info("handling ServiceAccount")
		err = m.client.Get(ctx, key, &corev1.ServiceAccount{})
	case *rbacv1.ClusterRole:
		log.Info("handling ClusterRole")
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
		err = m.client.Get(ctx, key, &rbacv1.ClusterRole{})
	case *rbacv1.ClusterRoleBinding:
		log.Info("handling ClusterRoleBinding")

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
		if err := m.client.Get(ctx, key, got); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("error while fetching %v: %w", v, err)
			}

			// Create the ClusterRoleBinding
			if err := m.client.Create(ctx, v); err != nil {
				return fmt.Errorf("error while creating %v: %w", v, err)
			}
			log.Info("ClusterRoleBinding created")

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
		if err := m.client.Patch(ctx, got, mergeFrom); err != nil {
			return fmt.Errorf("error while patching the `ClusterRoleBinding`: %w", err)
		}
		log.Info("ClusterRoleBinding patched")

		// we already patched the object, no need to go through the update path at the bottom of this function
		return nil
	case *corev1.ConfigMap:
		log.Info("handling ConfigMap")
		m.editConfigMap(v, namespace, m.options)
		err = m.client.Get(ctx, key, &corev1.ConfigMap{})
	case *corev1.Service:
		log.Info("handling Service")
		got := corev1.Service{}
		err = m.client.Get(ctx, key, &got)
		if err == nil {
			// Copy the ResourceVersion
			v.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
			// Copy the ClusterIP
			v.Spec.ClusterIP = got.Spec.ClusterIP
		}
	case *appsv1.Deployment:
		log.Info("handling Deployment")
		if len(v.Spec.Template.Spec.Containers) != 1 {
			log.Info("Unexpected number of containers in deployment, ignoring.")
		} else if m.options.OperatorImage != "" {
			log.Info("Patching operator image", "image", m.options.OperatorImage)
			v.Spec.Template.Spec.Containers[0].Image = m.options.OperatorImage
		}
		err = m.client.Get(ctx, key, &appsv1.Deployment{})
	default:
		return errs.New("unknown `client.Object`")
	}

	if err != nil {
		if errors.IsNotFound(err) {
			// the object (with that objectKey) does not exist yet, so we create it
			if err := m.client.Create(ctx, obj); err != nil {
				return fmt.Errorf("error while creating the `client.Object`: %w", err)
			}
			log.Info("new `client.Object` created")

			return nil
		}
		// something else went horribly wrong, abort
		return fmt.Errorf("error while fetching the `client.Object`: %w", err)
	}

	// if we made it this far, the object already exists, so we just update it
	if err := m.client.Update(ctx, obj); err != nil {
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
	// set the reference to our custom pod environment secret
	cm.Data["pod_environment_secret"] = PodEnvSecretName
	// set the list of inherited labels that will be passed on to the pods
	s := []string{pg.TenantLabelName, pg.ProjectIDLabelName, pg.UIDLabelName, pg.NameLabelName, pg.PartitionIDLabelName}
	// TODO maybe use a precompiled string here
	cm.Data["inherited_labels"] = strings.Join(s, ",")

	if options.DockerImage != "" {
		cm.Data["docker_image"] = options.DockerImage
	}

	if options.EtcdHost != "" {
		cm.Data["etcd_host"] = options.EtcdHost
	}

	cm.Data["enable_crd_registration"] = strconv.FormatBool(options.CRDRegistration)
	cm.Data["major_version_upgrade_mode"] = options.MajorVersionUpgradeMode

	// we specifically refer to those two users in the cloud-api, so we hardcode them here as well to be on the safe side.
	cm.Data["super_username"] = pg.PostresConfigSuperUsername
	cm.Data["replication_username"] = pg.PostgresConfigReplicationUsername

	cm.Data["enable_pod_antiaffinity"] = strconv.FormatBool(options.PodAntiaffinity)

	cm.Data["secret_name_template"] = "{username}.{cluster}.credentials"
	cm.Data["master_dns_name_format"] = "{cluster}.{team}.{hostedzone}"
	cm.Data["replica_dns_name_format"] = "{cluster}-repl.{team}.{hostedzone}"

	// set the spilo_fsgroup for correct tls cert permissions
	cm.Data["spilo_fsgroup"] = spilo_fsgroup

	cm.Data["enable_patroni_failsafe_mode"] = strconv.FormatBool(options.PatroniFailsafeMode)

	// override teams_api related defaults
	cm.Data["enable_teams_api"] = strconv.FormatBool(false)
	cm.Data["postgres_superuser_teams"] = ""
	cm.Data["teams_api_url"] = ""

}

// ensureCleanMetadata ensures obj has clean metadata
func (m *OperatorManager) ensureCleanMetadata(obj runtime.Object) error {
	// Remove annotations.
	if err := m.metadataAccessor.SetAnnotations(obj, nil); err != nil {
		return fmt.Errorf("error while removing annotations of the read k8s resource: %w", err)
	}

	// Remove resourceVersion.
	if err := m.metadataAccessor.SetResourceVersion(obj, ""); err != nil {
		return fmt.Errorf("error while removing resourceVersion of the read k8s resource: %w", err)
	}

	// Remove uid.
	if err := m.metadataAccessor.SetUID(obj, ""); err != nil {
		return fmt.Errorf("error while removing uid of the read k8s resource: %w", err)
	}

	return nil
}

// createOrUpdateNamespace ensures namespace exists
func (m *OperatorManager) createOrUpdateNamespace(ctx context.Context, namespace string) error {
	log := m.log.WithValues("ns", namespace)
	labels := map[string]string{
		pg.ManagedByLabelName: pg.ManagedByLabelValue,
		// TODO const and make configurable
		"pod-security.kubernetes.io/enforce": "privileged",
	}
	ns := corev1.Namespace{}
	if err := m.client.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return fmt.Errorf("error while fetching namespace %v: %w", namespace, err)
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = namespace
		nsObj.ObjectMeta.Labels = labels
		if err := m.client.Create(ctx, nsObj); err != nil {
			return fmt.Errorf("error while creating namespace %v: %w", namespace, err)
		}
		log.Info("namespace created")
	} else {
		// update namespace
		ns.ObjectMeta.Labels = labels
		if err := m.client.Update(ctx, &ns); err != nil {
			return fmt.Errorf("error while updating namespace: %w", err)
		}
		log.Info("namespace updated")
	}

	return nil
}

// CreatePodEnvironmentConfigMap creates a new ConfigMap with additional environment variables for the pods
func (m *OperatorManager) CreatePodEnvironmentConfigMap(ctx context.Context, namespace string) (*corev1.ConfigMap, error) {
	log := m.log.WithValues("ns", namespace)

	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      PodEnvCMName,
	}
	cm := &corev1.ConfigMap{}
	if err := m.client.Get(ctx, ns, cm); err == nil {
		// configmap already exists, nothing to do here
		// we will update the configmap with the correct S3 config in the postgres controller
		log.Info("Pod Environment ConfigMap already exists")
		return cm, nil
	}

	if err := m.metadataAccessor.SetName(cm, PodEnvCMName); err != nil {
		return nil, fmt.Errorf("error while setting the name of the new Pod Environment ConfigMap to %v: %w", PodEnvCMName, err)
	}
	if err := m.metadataAccessor.SetNamespace(cm, namespace); err != nil {
		return nil, fmt.Errorf("error while setting the namespace of the new Pod Environment ConfigMap to %v: %w", namespace, err)
	}

	if err := m.client.Create(ctx, cm); err != nil {
		return nil, fmt.Errorf("error while creating the new Pod Environment ConfigMap: %w", err)
	}
	log.Info("new Pod Environment ConfigMap created")

	return cm, nil
}

// CreateOrGetPodEnvironmentSecret creates a new Secret with additional environment variables for the pods or simply returns it if it already exists
func (m *OperatorManager) CreateOrGetPodEnvironmentSecret(ctx context.Context, namespace string) (*corev1.Secret, error) {
	log := m.log.WithValues("ns", namespace)

	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      PodEnvSecretName,
	}
	s := &corev1.Secret{}
	if err := m.client.Get(ctx, ns, s); err == nil {
		// secret already exists, nothing to do here
		// we will update the secret with the correct S3 config in the postgres controller
		log.Info("Pod Environment Secret already exists")
		return s, nil
	}

	if err := m.metadataAccessor.SetName(s, PodEnvSecretName); err != nil {
		return nil, fmt.Errorf("error while setting the name of the new Pod Environment Secret to %v: %w", namespace, err)
	}
	if err := m.metadataAccessor.SetNamespace(s, namespace); err != nil {
		return nil, fmt.Errorf("error while setting the namespace of the new Pod Environment Secret to %v: %w", namespace, err)
	}

	if err := m.client.Create(ctx, s); err != nil {
		return nil, fmt.Errorf("error while creating the new Pod Environment Secret: %w", err)
	}
	log.Info("new Pod Environment Secret created")

	return s, nil
}

func (m *OperatorManager) createOrUpdateSidecarsConfig(ctx context.Context, namespace string) error {
	// try to fetch the global sidecars configmap
	cns := types.NamespacedName{
		Namespace: m.options.PostgresletNamespace,
		Name:      m.options.SidecarsConfigMapName,
	}
	globalSidecarsCM := &corev1.ConfigMap{}
	if err := m.client.Get(ctx, cns, globalSidecarsCM); err != nil {
		// configmap with configuration does not exists, nothing we can do here...
		m.log.Error(err, "could not fetch global config for sidecars", "ns", namespace)
		return err
	}

	// Add our sidecars configmap
	if err := m.createOrUpdateSidecarsConfigMap(ctx, namespace, globalSidecarsCM); err != nil {
		return fmt.Errorf("error while creating sidecars configmap %v: %w", namespace, err)
	}

	return nil
}

// createOrUpdateSidecarsConfigMap Creates/updates a local ConfigMap for the sidecars, which e.g. contains the config files to mount in the sidecars
func (m *OperatorManager) createOrUpdateSidecarsConfigMap(ctx context.Context, namespace string, globalSidecarsCM *corev1.ConfigMap) error {
	log := m.log.WithValues("ns", namespace)
	sccm := &corev1.ConfigMap{}
	if err := m.metadataAccessor.SetName(sccm, localSidecarsCMName); err != nil {
		return fmt.Errorf("error while setting the name of the new Sidecars ConfigMap to %v: %w", namespace, err)
	}
	if err := m.metadataAccessor.SetNamespace(sccm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the new Sidecars ConfigMap to %v: %w", namespace, err)
	}

	// initialize map
	sccm.Data = make(map[string]string)

	// decode and write the fluentd.conf key from the global configmap to the local configmap
	b, err := base64.StdEncoding.DecodeString(globalSidecarsCM.Data["fluent-bit.conf"])
	if err == nil {

		sccm.Data[SidecarsCMFluentBitConfKey] = string(b)
	}

	// decode and write the queries.yaml key from the global configmap to the local configmap
	b, err = base64.StdEncoding.DecodeString(globalSidecarsCM.Data["queries.yaml"])
	if err == nil {
		sccm.Data[SidecarsCMExporterQueriesKey] = string(b)
	}

	// try to fetch any existing local sidecars configmap
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      localSidecarsCMName,
	}
	if err := m.client.Get(ctx, ns, &corev1.ConfigMap{}); err == nil {
		// local configmap already exists, updating it
		if err := m.client.Update(ctx, sccm); err != nil {
			return fmt.Errorf("error while updating the new Sidecars ConfigMap: %w", err)
		}
		log.Info("Sidecars ConfigMap updated")
		return nil
	}
	// todo: handle errors other than `NotFound`

	// local configmap does not exist, creating it
	if err := m.client.Create(ctx, sccm); err != nil {
		return fmt.Errorf("error while creating the new Sidecars ConfigMap: %w", err)
	}
	log.Info("new Sidecars ConfigMap created")

	return nil
}

func (m *OperatorManager) deletePodEnvironmentConfigMap(ctx context.Context, namespace string) error {
	cm := &corev1.ConfigMap{}
	if err := m.metadataAccessor.SetName(cm, PodEnvCMName); err != nil {
		return fmt.Errorf("error while setting the name of the Pod Environment ConfigMap to delete to %v: %w", PodEnvCMName, err)
	}
	if err := m.metadataAccessor.SetNamespace(cm, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the Pod Environment ConfigMap to delete to %v: %w", namespace, err)
	}
	if err := m.client.Delete(ctx, cm); err != nil {
		return fmt.Errorf("error while deleting the Pod Environment ConfigMap: %w", err)
	}
	m.log.Info("Pod Environment ConfigMap deleted", "ns", namespace)
	return nil
}

// toInstanceMatchingLabels makes the matching labels for the pods of the instances operated by the operator
func (m *OperatorManager) toInstanceMatchingLabels() *client.MatchingLabels {
	return &client.MatchingLabels{pg.ApplicationLabelName: pg.ApplicationLabelValue}
}

// toObjectKey makes ObjectKey from namespace and the name of obj
func (m *OperatorManager) toObjectKey(obj runtime.Object, namespace string) (client.ObjectKey, error) {
	name, err := m.metadataAccessor.Name(obj)
	if err != nil {
		return client.ObjectKey{}, fmt.Errorf("error while extracting the name of the k8s resource: %w", err)
	}
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, nil
}
