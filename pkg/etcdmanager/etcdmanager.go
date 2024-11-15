/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package etcdmanager

import (
	"context"
	errs "errors"
	"fmt"
	"os"
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
	"k8s.io/client-go/rest"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// etcdComponentLabelName Name of the managed-by label
	etcdComponentLabelName string = "postgres.database.fits.cloud/component"
	// etcdComponentLabelValue Value of the managed-by label
	etcdComponentLabelValue string = "etcd"
)

// Options
type Options struct {
	EtcdImage              string
	EtcdBackupSidecarImage string
	PostgresletNamespace   string
	PartitionID            string
	SecretKeyRefName       string
	PSPName                string
	PostgresletFullname    string
}

// OperatorManager manages the operator
type EtcdManager struct {
	client           client.Client
	decoder          runtime.Decoder
	manifest         *corev1.List
	log              logr.Logger
	metadataAccessor meta.MetadataAccessor
	scheme           *runtime.Scheme
	options          Options
}

// New creates a new `OperatorManager`
func New(confRest *rest.Config, fileName string, scheme *runtime.Scheme, log logr.Logger, opt Options) (*EtcdManager, error) {
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
	manifest := &corev1.List{}
	if _, _, err := deserializer.Decode(bb, nil, manifest); err != nil {
		return nil, fmt.Errorf("error while converting bytes to a list of yamls: %w", err)
	}

	log.Info("new `EtcdManager` created")
	return &EtcdManager{
		metadataAccessor: meta.NewAccessor(),
		client:           client,
		decoder:          deserializer,
		manifest:         manifest,
		scheme:           scheme,
		log:              log,
		options:          opt,
	}, nil
}

// InstallOrUpdateEtcd installs or updates the operator Stored in `OperatorManager`
func (m *EtcdManager) InstallOrUpdateEtcd() error {
	ctx := context.Background()

	// Decode each YAML to `client.Object`, add the namespace to it and install it.
	for _, item := range m.manifest.Items {
		obj, _, err := m.decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return fmt.Errorf("error while converting yaml to `client.Object`: %w", err)
		}

		cltObject, ok := obj.(client.Object)
		if !ok {
			return fmt.Errorf("unable to cast into client.Object")
		}
		if err := m.createNewClientObject(ctx, cltObject, m.options.PostgresletNamespace); err != nil {
			return fmt.Errorf("error while creating the `client.Object`: %w", err)
		}
	}

	if err := m.createOrUpdateServiceMonitor(ctx, "etcd-"+m.options.PostgresletFullname, "client"); err != nil {
		m.log.Error(err, "create/update of servicemonitor failed")
	}
	if err := m.createOrUpdateServiceMonitor(ctx, "etcd-"+m.options.PostgresletFullname+"-sidecar", "metrics"); err != nil {
		m.log.Error(err, "create/update of servicemonitor failed")
	}

	m.log.Info("etcd installed")
	return nil
}

// createNewClientObject adds namespace to obj and creates or patches it
func (m *EtcdManager) createNewClientObject(ctx context.Context, obj client.Object, namespace string) error {
	// remove any unwanted annotations, uids etc. Remember, these objects come straight from the YAML.
	if err := m.ensureCleanMetadata(obj); err != nil {
		return fmt.Errorf("error while ensuring the metadata of the `client.Object` is clean: %w", err)
	}

	// use our current namespace, not the one from the YAML
	if err := m.metadataAccessor.SetNamespace(obj, namespace); err != nil {
		return fmt.Errorf("error while setting the namespace of the `client.Object` to %v: %w", namespace, err)
	}

	// add common labels
	labels, err := m.metadataAccessor.Labels(obj)
	if err == nil {
		if nil == labels {
			labels = map[string]string{}
		}
		labels[pg.PartitionIDLabelName] = m.options.PartitionID
		labels[pg.ManagedByLabelName] = m.options.PostgresletFullname
		labels[etcdComponentLabelName] = etcdComponentLabelValue
		if err := m.metadataAccessor.SetLabels(obj, labels); err != nil {
			return fmt.Errorf("error while setting the labels of the `client.Object` to %v: %w", labels, err)
		}
	}

	// generate a proper object key for each object
	key, err := m.toObjectKey(obj, namespace)
	if err != nil {
		return fmt.Errorf("error while making the object key: %w", err)
	}

	stsName := "etcd-" + m.options.PostgresletFullname
	saName := stsName
	roleName := stsName
	rbName := stsName
	cmName := stsName
	svcName := stsName
	svcHeadlessName := svcName + "-headless"
	svcSidecarName := svcName + "-sidecar"

	// perform different modifications on the parsed objects based on their kind
	switch v := obj.(type) {

	case *corev1.ServiceAccount:
		m.log.Info("handling ServiceAccount")
		v.ObjectMeta.Name = saName

		// Use the updated name to get the resource
		key.Name = v.ObjectMeta.Name
		err = m.client.Get(ctx, key, &corev1.ServiceAccount{})

	case *rbacv1.Role:
		m.log.Info("handling Role")
		v.ObjectMeta.Name = roleName

		m.log.Info("Updating psp")
		for i := range v.Rules {
			i := i

			if !slices.Contains(v.Rules[i].APIGroups, "extensions") {
				continue
			}
			if !slices.Contains(v.Rules[i].Resources, "podsecuritypolicies") {
				continue
			}
			if !slices.Contains(v.Rules[i].Verbs, "use") {
				continue
			}
			// overwrite psp name
			v.Rules[i].ResourceNames = []string{m.options.PSPName}
		}

		// Use the updated name to get the resource
		key.Name = v.ObjectMeta.Name
		err = m.client.Get(ctx, key, &rbacv1.Role{})

	case *rbacv1.RoleBinding:
		m.log.Info("handling RoleBinding")
		v.ObjectMeta.Name = rbName

		m.log.Info("Updating roleRef")
		v.RoleRef.Name = roleName

		// Set the namespace of the ServiceAccount in the RoleBinding.
		for i, s := range v.Subjects {
			if s.Kind == "ServiceAccount" {
				v.Subjects[i].Name = saName
				v.Subjects[i].Namespace = namespace
			}
		}

		// Use the updated name to get the resource
		key.Name = v.ObjectMeta.Name
		err = m.client.Get(ctx, key, &rbacv1.RoleBinding{})

	case *corev1.ConfigMap:
		m.log.Info("handling ConfigMap")
		v.ObjectMeta.Name = cmName

		var configYaml strings.Builder
		configYaml.WriteString("db: etcd\n")
		configYaml.WriteString("db-data-directory: /data/etcd/\n")
		configYaml.WriteString("backup-provider: s3\n")
		configYaml.WriteString("backup-cron-schedule: \"*/1 * * * *\"\n")
		configYaml.WriteString("object-prefix: " + m.options.PartitionID + "\n")
		configYaml.WriteString("compression-method: tarlz4\n")
		configYaml.WriteString("post-exec-cmds:\n")
		configYaml.WriteString("- etcd --data-dir=/data/etcd --listen-metrics-urls http://0.0.0.0:2381\n")
		v.Data["config.yaml"] = configYaml.String()

		// Use the updated name to get the resource
		key.Name = v.ObjectMeta.Name
		err = m.client.Get(ctx, key, &corev1.ConfigMap{})

	case *appsv1.StatefulSet:
		m.log.Info("handling StatefulSet")
		v.ObjectMeta.Name = stsName

		m.log.Info("Updating containers")
		for i := range v.Spec.Template.Spec.Containers {
			i := i

			// Patch EtcdImage
			if m.options.EtcdImage != "" {
				m.log.Info("Updating etcd image")
				v.Spec.Template.Spec.Containers[i].Image = m.options.EtcdImage
			}

			m.log.Info("Updating envs")
			// Patch Env
			for j, env := range v.Spec.Template.Spec.Containers[i].Env {
				j := j
				env := env
				switch env.Name {
				case "BACKUP_RESTORE_SIDECAR_S3_BUCKET_NAME":
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
						env.ValueFrom.SecretKeyRef.Name = m.options.SecretKeyRefName
					}
				case "BACKUP_RESTORE_SIDECAR_S3_ENDPOINT":
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
						env.ValueFrom.SecretKeyRef.Name = m.options.SecretKeyRefName
					}
				case "BACKUP_RESTORE_SIDECAR_S3_REGION":
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
						env.ValueFrom.SecretKeyRef.Name = m.options.SecretKeyRefName
					}
				case "BACKUP_RESTORE_SIDECAR_S3_ACCESS_KEY":
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
						env.ValueFrom.SecretKeyRef.Name = m.options.SecretKeyRefName
					}
				case "BACKUP_RESTORE_SIDECAR_S3_SECRET_KEY":
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
						env.ValueFrom.SecretKeyRef.Name = m.options.SecretKeyRefName
					}
				case "ETCD_ADVERTISE_CLIENT_URLS":
					v.Spec.Template.Spec.Containers[i].Env[j].Value = "http://" + svcHeadlessName + "." + namespace + ".svc.cluster.local:2379,http://" + svcName + "." + namespace + ".svc.cluster.local:2379"
				case "ETCD_INITIAL_ADVERTISE_PEER_URLS":
					v.Spec.Template.Spec.Containers[i].Env[j].Value = "http://" + svcHeadlessName + "." + namespace + ".svc.cluster.local:2380"
				case "ETCD_INITIAL_CLUSTER":
					v.Spec.Template.Spec.Containers[i].Env[j].Value = "default=http://" + svcHeadlessName + "." + namespace + ".svc.cluster.local:2380"
				}
			}
		}

		// Patch EtcdBackupSidecarImage
		if m.options.EtcdBackupSidecarImage != "" {
			m.log.Info("Updating initContainers")
			for i := range v.Spec.Template.Spec.InitContainers {
				i := i

				m.log.Info("Updating etcd backup sidecar image")
				v.Spec.Template.Spec.InitContainers[i].Image = m.options.EtcdBackupSidecarImage
			}
		}

		m.log.Info("Updating configMap volume")
		for i := range v.Spec.Template.Spec.Volumes {
			i := i

			if v.Spec.Template.Spec.Volumes[i].Name != "backup-restore-sidecar-config" {
				continue
			}
			v.Spec.Template.Spec.Volumes[i].ConfigMap.Name = cmName
		}

		m.log.Info("Updating labels")
		// Add partition ID label
		v.Spec.Template.ObjectMeta.Labels[pg.PartitionIDLabelName] = m.options.PartitionID
		v.Spec.Template.ObjectMeta.Labels[pg.ManagedByLabelName] = m.options.PostgresletFullname
		v.Spec.Template.ObjectMeta.Labels[etcdComponentLabelName] = etcdComponentLabelValue
		v.Spec.Template.ObjectMeta.Labels[pg.NameLabelName] = stsName

		m.log.Info("Updating selector")
		// spec.selector.matchLabels
		v.Spec.Selector.MatchLabels[pg.PartitionIDLabelName] = m.options.PartitionID
		v.Spec.Selector.MatchLabels[pg.ManagedByLabelName] = m.options.PostgresletFullname
		v.Spec.Selector.MatchLabels[etcdComponentLabelName] = etcdComponentLabelValue
		v.Spec.Selector.MatchLabels[pg.NameLabelName] = stsName

		m.log.Info("Updating serviceName")
		// spec.serviceName
		v.Spec.ServiceName = stsName + "-client"

		m.log.Info("Updating serviceAccountName")
		// spec.serviceName
		v.Spec.Template.Spec.ServiceAccountName = saName

		got := appsv1.StatefulSet{}
		// Use the updated name to get the resource
		key.Name = v.ObjectMeta.Name
		err = m.client.Get(ctx, key, &got)
		if err == nil {
			// Copy the ResourceVersion
			m.log.Info("Copying existing resource version")
			v.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
		}

	case *corev1.Service:
		m.log.Info("handling Service")
		switch v.ObjectMeta.Name {
		case "backup-restore-sidecar-svc":
			v.ObjectMeta.Name = svcSidecarName
		case "etcd-psql-headless":
			v.ObjectMeta.Name = svcHeadlessName
		case "etcd-psql":
			v.ObjectMeta.Name = svcName
		default:
			return fmt.Errorf("unknown service name: %v", v.ObjectMeta.Name)
		}

		m.log.Info("Updating labels")
		v.ObjectMeta.Labels[pg.NameLabelName] = v.ObjectMeta.Name

		m.log.Info("Updating selector")
		v.Spec.Selector[pg.PartitionIDLabelName] = m.options.PartitionID
		v.Spec.Selector[pg.ManagedByLabelName] = m.options.PostgresletFullname
		v.Spec.Selector[pg.NameLabelName] = stsName

		got := corev1.Service{}
		key.Name = v.ObjectMeta.Name
		err = m.client.Get(ctx, key, &got)
		if err == nil {
			// Copy the ResourceVersion
			v.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
			// Copy the ClusterIP
			v.Spec.ClusterIP = got.Spec.ClusterIP
		}

	default:
		return errs.New("unknown `client.Object`")
	}

	if err != nil {
		if errors.IsNotFound(err) {
			// the object (with that objectKey) does not exist yet, so we create it
			if err := m.client.Create(ctx, obj); err != nil {
				return fmt.Errorf("error while creating the `client.Object`: %w", err)
			}
			m.log.Info("new `client.Object` created")

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

// ensureCleanMetadata ensures obj has clean metadata
func (m *EtcdManager) ensureCleanMetadata(obj runtime.Object) error {
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

// toObjectKey makes ObjectKey from namespace and the name of obj
func (m *EtcdManager) toObjectKey(obj runtime.Object, namespace string) (client.ObjectKey, error) {
	name, err := m.metadataAccessor.Name(obj)
	if err != nil {
		return client.ObjectKey{}, fmt.Errorf("error while extracting the name of the k8s resource: %w", err)
	}
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, nil
}

// createOrUpdateExporterSidecarServiceMonitor ensures the servicemonitors for the sidecars exist
func (m *EtcdManager) createOrUpdateServiceMonitor(ctx context.Context, targetName string, targetEndpoint string) error {
	ns := m.options.PostgresletNamespace
	n := targetName + "-svcm"
	sm := &coreosv1.ServiceMonitor{}

	// TODO what's the correct name?
	if err := m.metadataAccessor.SetName(sm, n); err != nil {
		return fmt.Errorf("error while setting the name of the servicemonitor to %v: %w", ns, err)
	}
	if err := m.metadataAccessor.SetNamespace(sm, ns); err != nil {
		return fmt.Errorf("error while setting the namespace of the servicemonitor to %v: %w", ns, err)
	}
	l := map[string]string{
		pg.PartitionIDLabelName: m.options.PartitionID,
		pg.ManagedByLabelName:   m.options.PostgresletFullname,
		etcdComponentLabelName:  etcdComponentLabelValue,
		pg.NameLabelName:        n,
	}
	if err := m.metadataAccessor.SetLabels(sm, l); err != nil {
		return fmt.Errorf("error while setting the namespace of the servicemonitor to %v: %w", ns, err)
	}

	sm.Spec.Endpoints = []coreosv1.Endpoint{
		{
			Port: targetEndpoint,
		},
	}
	sm.Spec.NamespaceSelector = coreosv1.NamespaceSelector{
		MatchNames: []string{ns},
	}
	l[pg.NameLabelName] = targetName
	sm.Spec.Selector = metav1.LabelSelector{
		MatchLabels: l,
	}

	// try to fetch any existing service
	nsn := types.NamespacedName{
		Namespace: ns,
		Name:      n,
	}
	old := &coreosv1.ServiceMonitor{}
	if err := m.client.Get(ctx, nsn, old); err == nil {
		// Copy the resource version
		sm.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
		if err := m.client.Update(ctx, sm); err != nil {
			return fmt.Errorf("error while updating the servicemonitor: %w", err)
		}
		m.log.Info("servicemonitor updated")
		return nil
	}

	// local servicemonitor does not exist, creating it
	if err := m.client.Create(ctx, sm); err != nil {
		return fmt.Errorf("error while creating the servicemonitor: %w", err)
	}
	m.log.Info("servicemonitor created")

	return nil
}

func (m *EtcdManager) UninstallEtcd() error {

	ctx := context.Background()

	matchingLabels := client.MatchingLabels{
		pg.PartitionIDLabelName: m.options.PartitionID,
		pg.ManagedByLabelName:   m.options.PostgresletFullname,
		etcdComponentLabelName:  etcdComponentLabelValue,
	}
	deleteAllOpts := []client.DeleteAllOfOption{
		client.InNamespace(m.options.PostgresletNamespace),
		matchingLabels,
	}

	// StatefulSet
	if err := m.client.DeleteAllOf(ctx, &appsv1.StatefulSet{}, deleteAllOpts...); err != nil {
		if !errors.IsNotFound(err) {
			m.log.Error(err, "Could not delete StatefulSet")
		}
	}

	// ConfigMap
	if err := m.client.DeleteAllOf(ctx, &corev1.ConfigMap{}, deleteAllOpts...); err != nil {
		if !errors.IsNotFound(err) {
			m.log.Error(err, "Could not delete ConfigMap")
		}
	}

	// RoleBinding
	if err := m.client.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, deleteAllOpts...); err != nil {
		if !errors.IsNotFound(err) {
			m.log.Error(err, "Could not delete RoleBinding")
		}
	}

	// Role
	if err := m.client.DeleteAllOf(ctx, &rbacv1.Role{}, deleteAllOpts...); err != nil {
		if !errors.IsNotFound(err) {
			m.log.Error(err, "Could not delete Role")
		}
	}

	// ServiceAccount
	if err := m.client.DeleteAllOf(ctx, &corev1.ServiceAccount{}, deleteAllOpts...); err != nil {
		if !errors.IsNotFound(err) {
			m.log.Error(err, "Could not delete ServiceAccount")
		}
	}

	// Service
	if err := m.client.DeleteAllOf(ctx, &corev1.Service{}, deleteAllOpts...); err != nil {
		if !errors.IsNotFound(err) {
			m.log.Error(err, "Could not delete Service")
		}
	}

	return nil
}
