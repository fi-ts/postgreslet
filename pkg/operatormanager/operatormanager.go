/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package operatormanager

import (
	"context"
	errs "errors"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The serviceAccount name to use for the database pods.
// TODO: create new account per namespace
// TODO: use different account for operator and database
const serviceAccountName string = "postgres-operator"

// PodEnvCMName Name of the pod environment configmap to create and use
const PodEnvCMName string = "postgres-pod-config"

const operatorPodLabelName string = "name"
const operatorPodLabelValue string = "postgres-operator"

// operatorPodMatchingLabels is for listing operator pods
var operatorPodMatchingLabels = client.MatchingLabels{operatorPodLabelName: operatorPodLabelValue}

// OperatorManager manages the operator
type OperatorManager struct {
	client.Client
	runtime.Decoder
	list *corev1.List
	log  logr.Logger
	meta.MetadataAccessor
	*runtime.Scheme
	pspName string
}

// New creates a new `OperatorManager`
func New(conf *rest.Config, fileName string, scheme *runtime.Scheme, log logr.Logger, pspName string) (*OperatorManager, error) {
	// Use no-cache client to avoid waiting for cashing.
	client, err := client.New(conf, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("error while creating new k8s client: %w", err)
	}

	bb, err := ioutil.ReadFile(fileName)
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
		pspName:          pspName,
	}, nil
}

// InstallOrUpdateOperator installs or updates the operator Stored in `OperatorManager`
func (m *OperatorManager) InstallOrUpdateOperator(ctx context.Context, namespace string) ([]client.Object, error) {
	objs := []client.Object{}

	// Make sure the namespace exists.
	objs, err := m.ensureNamespace(ctx, namespace, objs)
	if err != nil {
		return objs, fmt.Errorf("error while ensuring the existence of namespace %v: %w", namespace, err)
	}

	// Add our (initially empty) custom pod environment configmap
	objs, err = m.ensurePodEnvironmentConfigMap(ctx, namespace, objs)
	if err != nil {
		return objs, fmt.Errorf("error while creating pod environment configmap %v: %w", namespace, err)
	}

	// Add our postgres exporter environment configmap
	objs, err = m.ensureMonitoringConfigMap(ctx, namespace, objs)
	if err != nil {
		return objs, fmt.Errorf("error while creating postgres exporter configmap %v: %w", namespace, err)
	}

	// Add our FluentD configmap
	objs, err = m.ensureLoggingConfigMap(ctx, namespace, objs)
	if err != nil {
		return objs, fmt.Errorf("error while creating FluentD configmap %v: %w", namespace, err)
	}

	// Decode each YAML to `client.Object`, add the namespace to it and install it.
	for _, item := range m.list.Items {
		obj, _, err := m.Decoder.Decode(item.Raw, nil, nil)
		if err != nil {
			return objs, fmt.Errorf("error while converting yaml to `client.Object`: %w", err)
		}

		cltObject, ok := obj.(client.Object)
		if !ok {
			return objs, fmt.Errorf("unable to cast into client.Object")
		}
		if objs, err := m.createNewClientObject(ctx, objs, cltObject, namespace); err != nil {
			return objs, fmt.Errorf("error while creating the `client.Object`: %w", err)
		}
	}

	if err = m.waitTillOperatorReady(ctx, time.Minute, time.Second); err != nil {
		return objs, fmt.Errorf("error while waiting for the readiness of the operator: %w", err)
	}

	m.log.Info("operator installed")
	return objs, nil
}

// IsOperatorDeletable returns true when there's no running instance operated by the operator
func (m *OperatorManager) IsOperatorDeletable(ctx context.Context, namespace string) (bool, error) {
	setList := &appsv1.StatefulSetList{}
	if err := m.List(ctx, setList, client.InNamespace(namespace), m.toInstanceMatchingLabels(namespace)); client.IgnoreNotFound(err) != nil {
		return false, fmt.Errorf("error while fetching the list of statefulsets operated by the operator: %w", err)
	}
	if setList != nil && len(setList.Items) != 0 {
		m.log.Info("statefulset still running")
		return false, nil
	}

	services := &corev1.ServiceList{}
	if err := m.List(ctx, services, client.InNamespace(namespace), m.toInstanceMatchingLabels(namespace)); client.IgnoreNotFound(err) != nil {
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

		if err := m.SetNamespace(obj, namespace); err != nil {
			return fmt.Errorf("error while setting the namesapce: %w", err)
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
						return fmt.Errorf("error while patching %v: %w", v, err)
					}
				}
			}
		default:
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

	// delete pod environment configmap
	if err := m.deletePodEnvironmentConfigMap(ctx, namespace); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("error while deleting pod environment configmap: %w", err)
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
func (m *OperatorManager) createNewClientObject(ctx context.Context, objs []client.Object, obj client.Object, namespace string) ([]client.Object, error) {
	// remove any unwanted annotations, uids etc. Remember, these objects come straight from the YAML.
	if err := m.ensureCleanMetadata(obj); err != nil {
		return objs, fmt.Errorf("error while ensuring the metadata of the `client.Object` is clean: %w", err)
	}

	// use our current namespace, not the one from the YAML
	if err := m.SetNamespace(obj, namespace); err != nil {
		return objs, fmt.Errorf("error while setting the namespace of the `client.Object` to %v: %w", namespace, err)
	}

	// generate a proper object key for each object
	key, err := m.toObjectKey(obj, namespace)
	if err != nil {
		return objs, fmt.Errorf("error while making the object key: %w", err)
	}

	// perform different modifications on the parsed objects based on their kind
	switch v := obj.(type) {
	case *v1.ServiceAccount:
		m.log.Info("handling ServiceAccount")
		err = m.Get(ctx, key, &v1.ServiceAccount{})
	case *rbacv1.ClusterRole:
		m.log.Info("handling ClusterRole")
		// Add our psp
		pspPolicyRule := rbacv1.PolicyRule{
			APIGroups:     []string{"extensions"},
			Verbs:         []string{"use"},
			Resources:     []string{"podsecuritypolicies"},
			ResourceNames: []string{m.pspName},
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

		// If a ClusterRoleBinding already exists, patch it.
		got := &rbacv1.ClusterRoleBinding{}
		err = m.Get(ctx, key, got)
		if err == nil {
			patch := client.MergeFrom(got.DeepCopy())
			v.Subjects = append(got.Subjects, v.Subjects[0])
			if err := m.Patch(ctx, v, patch); err != nil {
				return objs, fmt.Errorf("error while patching the `ClusterRoleBinding`: %w", err)
			}
			m.log.Info("ClusterRoleBinding patched")
			// we already patched the object, no need to go through the update path at the bottom of this function
			return objs, nil
		}
	case *v1.ConfigMap:
		m.log.Info("handling ConfigMap")
		m.editConfigMap(v, namespace)
		err = m.Get(ctx, key, &v1.ConfigMap{})
	case *v1.Service:
		m.log.Info("handling Service")
		got := v1.Service{}
		err = m.Get(ctx, key, &got)
		if err == nil {
			// Copy the ResourceVersion
			v.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
			// Copy the ClusterIP
			v.Spec.ClusterIP = got.Spec.ClusterIP
		}
	case *appsv1.Deployment:
		m.log.Info("handling Deployment")
		err = m.Get(ctx, key, &appsv1.Deployment{})
	default:
		return objs, errs.New("unknown `client.Object`")
	}

	if err != nil {
		if errors.IsNotFound(err) {
			// the object (with that objectKey) does not exist yet, so we create it
			if err := m.Create(ctx, obj); err != nil {
				return objs, fmt.Errorf("error while creating the `client.Object`: %w", err)
			}
			m.log.Info("new `client.Object` created")

			// Append the newly created obj.
			objs = append(objs, obj)
			return objs, nil
		}
		// something else went horribly wrong, abort
		return objs, fmt.Errorf("error while fetching the `client.Object`: %w", err)
	}

	// if we made it this far, the object already exists, so we just update it
	if err := m.Update(ctx, obj); err != nil {
		return objs, fmt.Errorf("error while updating the `client.Object`: %w", err)
	}

	return objs, nil
}

// editConfigMap adds info to cm
func (m *OperatorManager) editConfigMap(cm *v1.ConfigMap, namespace string) {
	cm.Data["watched_namespace"] = namespace
	// TODO don't use the same serviceaccount for operator and databases, see #88
	cm.Data["pod_service_account_name"] = serviceAccountName
	// set the reference to our custom pod environment configmap
	cm.Data["pod_environment_configmap"] = PodEnvCMName
	// set the list of inherited labels that will be passed on to the pods
	s := []string{pg.TenantLabelName, pg.ProjectIDLabelName}
	// TODO maybe use a precompiled string here
	cm.Data["inherited_labels"] = strings.Join(s, ",")
	// TODO additional vars
	// debug_logging: "true"
	// docker_image: "{{ versions.spiloImage }}"
	// enable_ebs_gp3_migration: "false"
	// enable_master_load_balancer: "false"
	// enable_pgversion_env_var: "true"
	// enable_pod_disruption_budget: "true"
	// enable_replica_load_balancer: "false"
	// enable_sidecars: "true"
	// enable_spilo_wal_path_compat: "true"
	// enable_teams_api: "true"
	// external_traffic_policy: "Cluster"
	// logical_backup_docker_image: "{{ versions.logical_backupImage }}"
	// log_s3_bucket: "{{ s3bucket.log_s3_bucket }}"
	// wal_s3_bucket: "{{ s3bucket.wal_s3_bucket }}"
	// logical_backup_s3_access_key_id: "{{ s3bucket.logical_backup_s3_access_key_id }}"
	// logical_backup_s3_bucket: "{{ s3bucket.logical_backup_s3_bucket }}"
	// logical_backup_s3_endpoint: "{{ s3bucket.logical_backup_s3_endpoint }}"
	// logical_backup_s3_secret_access_key: "{{ s3bucket.logical_backup_s3_secret_access_key }}"
	// logical_backup_s3_sse: "{{ s3bucket.logical_backup_s3_sse }}"
	// logical_backup_schedule: "{{ s3bucket.logical_backup_schedule }}"
	// master_dns_name_format: "{cluster}.{team}.{hostedzone}"
	// pam_role_name: "{{ authentication.pam_role_name }}"
	// pdb_name_format: "postgres-{cluster}-pdb"
	// pod_deletion_wait_timeout: 10m
	// pod_environment_configmap: "{{ pod_environment_cm.pod_environment_configmap }}"
	// pod_label_wait_timeout: 10m
	// pod_management_policy: "ordered_ready"
	// pod_role_label: spilo-role
	// pod_terminate_grace_period: 5m
	// postgres_superuser_teams: "postgres_superusers"
	// protected_role_names: "admin"
	// ready_wait_interval: 3s
	// ready_wait_timeout: 30s
	// repair_period: 5m
	// replica_dns_name_format: "{cluster}-repl.{team}.{hostedzone}"
	// replication_username: standby
	// resource_check_interval: 3s
	// resource_check_timeout: 10m
	// resync_period: 30m
	// ring_log_lines: "100"
	// secret_name_template: "{username}.{cluster}.credentials"
	// spilo_privileged: "false"
	// storage_resize_mode: "pvc"
	// super_username: postgres
	// team_admin_role: "admin"

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

// ensureNamespace ensures namespace exists
func (m *OperatorManager) ensureNamespace(ctx context.Context, namespace string, objs []client.Object) ([]client.Object, error) {
	if err := m.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{}); err != nil {
		// errors other than `not found`
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("error while fetching namespace %v: %w", namespace, err)
		}

		// Create the namespace.
		nsObj := &corev1.Namespace{}
		nsObj.Name = namespace
		nsObj.ObjectMeta.Labels = map[string]string{
			pg.ManagedByLabelName: pg.ManagedByLabelValue,
		}
		if err := m.Create(ctx, nsObj); err != nil {
			return nil, fmt.Errorf("error while creating namespace %v: %w", namespace, err)
		}

		// Append the created namespace to the list of the created `client.Object`s.
		objs = append(objs, nsObj)
	}

	return objs, nil
}

// createPodEnvironmentConfigMap creates a new ConfigMap with additional environment variables for the pods
func (m *OperatorManager) ensurePodEnvironmentConfigMap(ctx context.Context, namespace string, objs []client.Object) ([]client.Object, error) {
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      PodEnvCMName,
	}
	if err := m.Get(ctx, ns, &v1.ConfigMap{}); err == nil {
		// configmap already exists, nothing to do here
		m.log.Info("Pod Environment ConfigMap already exists")
		return objs, nil
	}

	cm := &v1.ConfigMap{}
	if err := m.SetName(cm, PodEnvCMName); err != nil {
		return objs, fmt.Errorf("error while setting the name of the new Pod Environment ConfigMap to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(cm, namespace); err != nil {
		return objs, fmt.Errorf("error while setting the namespace of the new Pod Environment ConfigMap to %v: %w", namespace, err)
	}

	if err := m.Create(ctx, cm); err != nil {
		return objs, fmt.Errorf("error while creating the new Pod Environment ConfigMap: %w", err)
	}
	m.log.Info("new Pod Environment ConfigMap created")

	return objs, nil
}

func (m *OperatorManager) ensureLoggingConfigMap(ctx context.Context, namespace string, objs []client.Object) ([]client.Object, error) {
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      pg.FluentDCMName,
	}
	if err := m.Get(ctx, ns, &v1.ConfigMap{}); err == nil {
		// TODO implement update
		// configmap already exists, nothing to do here
		m.log.Info("FluentD ConfigMap already exists")
		return objs, nil
	}

	cm := &v1.ConfigMap{}
	if err := m.SetName(cm, pg.FluentDCMName); err != nil {
		return objs, fmt.Errorf("error while setting the name of the new FluentD ConfigMap to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(cm, namespace); err != nil {
		return objs, fmt.Errorf("error while setting the namespace of the new FluentD ConfigMap to %v: %w", namespace, err)
	}

	cm.Data = make(map[string]string)
	cm.Data["fluentd.conf"] = `<source>
	type tail
	path /home/postgres/pgdata/pgroot/pg_log/*.csv
	pos_file /home/postgres/pgdata/pgroot/pg_log/postgresql.csv.pos
	tag psqlcsv
	format none
  </source>
  
  <source>
	type tail
	path /home/postgres/pgdata/pgroot/pg_log/*.log
	pos_file /home/postgres/pgdata/pgroot/pg_log/postgresql.log.pos
	tag psqllog
	format none
  </source>
  
  <match **>
	@type stdout
  </match>`

	if err := m.Create(ctx, cm); err != nil {
		return objs, fmt.Errorf("error while creating the new FluentD ConfigMap: %w", err)
	}
	m.log.Info("new FluentD ConfigMap created")

	return objs, nil
}

func (m *OperatorManager) ensureMonitoringConfigMap(ctx context.Context, namespace string, objs []client.Object) ([]client.Object, error) {
	ns := types.NamespacedName{
		Namespace: namespace,
		Name:      pg.PostgresExporterCMName,
	}
	if err := m.Get(ctx, ns, &v1.ConfigMap{}); err == nil {
		// TODO implement update
		// configmap already exists, nothing to do here
		m.log.Info("Postgres Exporter ConfigMap already exists")
		return objs, nil
	}

	cm := &v1.ConfigMap{}
	if err := m.SetName(cm, pg.PostgresExporterCMName); err != nil {
		return objs, fmt.Errorf("error while setting the name of the new Postgres Exporter ConfigMap to %v: %w", namespace, err)
	}
	if err := m.SetNamespace(cm, namespace); err != nil {
		return objs, fmt.Errorf("error while setting the namespace of the new Postgres Exporter ConfigMap to %v: %w", namespace, err)
	}

	cm.Data = make(map[string]string)
	cm.Data["queries.yaml"] = `pg_postmaster:
	query: "SELECT pg_postmaster_start_time as start_time_seconds from pg_postmaster_start_time()"
	master: true
	metrics:
	- start_time_seconds:
		usage: "GAUGE"
		description: "Time at which postmaster started"
	
	pg_is_in_recovery:
	query: "SELECT CASE WHEN pg_is_in_recovery = true THEN 1 ELSE 2 END AS status from pg_is_in_recovery();"
	metrics:
	- status:
		usage: "GAUGE"
		description: "Return value of 1 means database is in recovery. Otherwise 2 it is a primary."
	
	pg_replication_lag:
	query: "SELECT
			CASE
			WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0
			ELSE EXTRACT (EPOCH FROM now() - pg_last_xact_replay_timestamp())::INTEGER
			END
		AS replay_time"
	metrics:
	- replay_time:
		usage: "GAUGE"
		description: "Length of time since the last transaction was replayed on replica. Will always increase if no writes on primary."
	
	pg_replication_global_status:
	query: "SELECT (extract(epoch from now()) * 1e9)::int8 as epoch_ns, application_name as tag_application_name,
			concat(coalesce(client_addr::text, client_hostname), '_', client_port::text) as tag_client_info,
			coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), write_lsn)::int8, 0) as write_lag_b,
			coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), flush_lsn)::int8, 0) as flush_lag_b,
			coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)::int8, 0) as replay_lag_b,
			state,
			sync_state,
			case when sync_state in ('sync', 'quorum') then 1 else 0 end as is_sync_int
		from
			pg_catalog.pg_stat_replication"
	metrics:
	- tag_application_name:
		usage: "LABEL"
		description: "Replication Database (Standby)"
	- tag_client_info:
		usage: "LABEL"
		description: "Replication Client Info (Standby)"
	- state:
		usage: "LABEL"
		description: "Replication State"
	- sync_state:
		usage: "LABEL"
		description: "Replication Sync State"
	- write_lag_b:
		usage: "GAUGE"
		description: "Replication Write Lag Master"
	- flush_lag_b:
		usage: "GAUGE"
		description: "Replication Flush Lag Master"
	- replay_lag_b:
		usage: "GAUGE"
		description: "Replication Replay Lag Master"
	
	pg_replication_global_status_standby:
	query: "select
			(extract(epoch from now()) * 1e9)::int8 as epoch_ns,
			pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()) as replay_lag_b,
			extract(epoch from (now() - pg_last_xact_replay_timestamp()))::int8 as last_replay_s;"
	metrics: 
	- replay_lag_b:
		usage: "GAUGE"
		description: "Replication Replay Lag Standby"
	- last_replay_s:
		usage: "GAUGE"
		description: "Last Replication Lag Time Standby"
	
	
	pg_replication_lag_size:
	query: "SELECT client_addr as replica
		, client_hostname as replica_hostname
		, client_port as replica_port
		, pg_wal_lsn_diff(sent_lsn, replay_lsn) as bytes 
		FROM pg_catalog.pg_stat_replication"
	metrics:
	- replica:
		usage: "LABEL"
		description: "Replica address"
	- replica_hostname:
		usage: "LABEL"
		description: "Replica hostname"
	- replica_port:
		usage: "LABEL"
		description: "Replica port"
	- bytes:
		usage: "GAUGE"
		description: "Replication lag in bytes"
	
	pg_replication_slots:
	query: "SELECT slot_name, active::int, pg_wal_lsn_diff(pg_current_wal_insert_lsn(), restart_lsn) AS retained_bytes FROM pg_catalog.pg_replication_slots"
	metrics:
	- slot_name:
		usage: "LABEL"
		description: "Name of replication slot"
	- active: 
		usage: "GAUGE" 
		description: "Active state of slot. 1 = true. 0 = false."
	- retained_bytes:
		usage: "GAUGE"
		description: "The amount of WAL (in bytes) being retained for this slot"
	
	pg_wal_activity:
	query: "SELECT last_5_min_size_bytes,
		(SELECT COALESCE(sum(size),0) FROM pg_catalog.pg_ls_waldir()) AS total_size_bytes
		FROM (SELECT COALESCE(sum(size),0) AS last_5_min_size_bytes FROM pg_catalog.pg_ls_waldir() WHERE modification > CURRENT_TIMESTAMP - '5 minutes'::interval) x;"
	metrics:
	- last_5_min_size_bytes:
		usage: "GAUGE"
		description: "Current size in bytes of the last 5 minutes of WAL generation. Includes recycled WALs."
	- total_size_bytes:
		usage: "GAUGE"
		description: "Current size in bytes of the WAL directory"
	
	pg_stat_wal_receiver:
	query: |
	SELECT case status when 'stopped' then 0 when 'starting' then 1 when 'streaming' then 2 when 'waiting' then 3 when 'restarting' then 4 when 'stopping' then 5 else -1 end as status,
			(receive_start_lsn- '0/0') % (2^52)::bigint as receive_start_lsn,
			receive_start_tli,
			(received_lsn- '0/0') % (2^52)::bigint as received_lsn,
			received_tli,
			extract(epoch from last_msg_send_time) as last_msg_send_time,
			extract(epoch from last_msg_receipt_time) as last_msg_receipt_time,
			(latest_end_lsn - '0/0') % (2^52)::bigint as latest_end_lsn,
			extract(epoch from latest_end_time) as latest_end_time,
			substring(slot_name from 'repmgr_slot_([0-9]*)') as upstream_node,
			trim(both '''' from substring(conninfo from 'host=([^ ]*)')) as upstream_host,
			slot_name
		FROM pg_stat_wal_receiver
	metrics:
	- status:
		usage: "GAUGE"
		description: "Activity status of the WAL receiver process (0=stopped 1=starting 2=streaming 3=waiting 4=restarting 5=stopping)"
	- receive_start_lsn:
		usage: "COUNTER"
		description: "First transaction log position used when WAL receiver is started"
	- receive_start_tli:
		usage: "GAUGE"
		description: "First timeline number used when WAL receiver is started"
	- received_lsn:
		usage: "COUNTER"
		description: "Last transaction log position already received and flushed to disk, the initial value of this field being the first log position used when WAL receiver is started"
	- received_tli:
		usage: "GAUGE"
		description: "Timeline number of last transaction log position received and flushed to disk, the initial value of this field being the timeline number of the first log position used when WAL receiver is started"
	- last_msg_send_time:
		usage: "COUNTER"
		description: "Send time of last message received from origin WAL sender"
	- last_msg_receipt_time:
		usage: "COUNTER"
		description: "Receipt time of last message received from origin WAL sender"
	- latest_end_lsn:
		usage: "COUNTER"
		description: "Last transaction log position reported to origin WAL sender"
	- latest_end_time:
		usage: "COUNTER"
		description: "Time of last transaction log position reported to origin WAL sender"
	- upstream_node:
		usage: "GAUGE"
		description: "The repmgr node from the upstream slot name"
	- upstream_host:
		usage: "LABEL"
		description: "The upstream host this node is replicating from"
	- slot_name:
		usage: "LABEL"
		description: "The upstream slot_name this node is replicating from"
	
	pg_archive_command_status:
	query: "SELECT CASE 
	WHEN EXTRACT(epoch from (last_failed_time - last_archived_time)) IS NULL THEN 0
	WHEN EXTRACT(epoch from (last_failed_time - last_archived_time)) < 0 THEN 0
	ELSE EXTRACT(epoch from (last_failed_time - last_archived_time)) 
	END AS seconds_since_last_fail
	FROM pg_catalog.pg_stat_archiver"
	metrics:
	- seconds_since_last_fail:
		usage: "GAUGE"
		description: "Seconds since the last recorded failure of the archive_command"
	
	pg_stat_user_tables:
	query: "SELECT current_database() datname, schemaname, relname, seq_scan, seq_tup_read, idx_scan, idx_tup_fetch, n_tup_ins, n_tup_upd, n_tup_del, n_tup_hot_upd, n_live_tup, n_dead_tup, n_mod_since_analyze, COALESCE(last_vacuum, '1970-01-01Z'), COALESCE(last_vacuum, '1970-01-01Z') as last_vacuum, COALESCE(last_autovacuum, '1970-01-01Z') as last_autovacuum, COALESCE(last_analyze, '1970-01-01Z') as last_analyze, COALESCE(last_autoanalyze, '1970-01-01Z') as last_autoanalyze, vacuum_count, autovacuum_count, analyze_count, autoanalyze_count FROM pg_stat_user_tables"
	metrics:
	- datname:
		usage: "LABEL"
		description: "Name of current database"
	- schemaname:
		usage: "LABEL"
		description: "Name of the schema that this table is in"
	- relname:
		usage: "LABEL"
		description: "Name of this table"
	- seq_scan:
		usage: "COUNTER"
		description: "Number of sequential scans initiated on this table"
	- seq_tup_read:
		usage: "COUNTER"
		description: "Number of live rows fetched by sequential scans"
	- idx_scan:
		usage: "COUNTER"
		description: "Number of index scans initiated on this table"
	- idx_tup_fetch:
		usage: "COUNTER"
		description: "Number of live rows fetched by index scans"
	- n_tup_ins:
		usage: "COUNTER"
		description: "Number of rows inserted"
	- n_tup_upd:
		usage: "COUNTER"
		description: "Number of rows updated"
	- n_tup_del:
		usage: "COUNTER"
		description: "Number of rows deleted"
	- n_tup_hot_upd:
		usage: "COUNTER"
		description: "Number of rows HOT updated (i.e., with no separate index update required)"
	- n_live_tup:
		usage: "GAUGE"
		description: "Estimated number of live rows"
	- n_dead_tup:
		usage: "GAUGE"
		description: "Estimated number of dead rows"
	- n_mod_since_analyze:
		usage: "GAUGE"
		description: "Estimated number of rows changed since last analyze"
	- last_vacuum:
		usage: "GAUGE"
		description: "Last time at which this table was manually vacuumed (not counting VACUUM FULL)"
	- last_autovacuum:
		usage: "GAUGE"
		description: "Last time at which this table was vacuumed by the autovacuum daemon"
	- last_analyze:
		usage: "GAUGE"
		description: "Last time at which this table was manually analyzed"
	- last_autoanalyze:
		usage: "GAUGE"
		description: "Last time at which this table was analyzed by the autovacuum daemon"
	- vacuum_count:
		usage: "COUNTER"
		description: "Number of times this table has been manually vacuumed (not counting VACUUM FULL)"
	- autovacuum_count:
		usage: "COUNTER"
		description: "Number of times this table has been vacuumed by the autovacuum daemon"
	- analyze_count:
		usage: "COUNTER"
		description: "Number of times this table has been manually analyzed"
	- autoanalyze_count:
		usage: "COUNTER"
		description: "Number of times this table has been analyzed by the autovacuum daemon"
	
	pg_statio_user_tables:
	query: "SELECT current_database() datname, schemaname, relname, heap_blks_read, heap_blks_hit, idx_blks_read, idx_blks_hit, toast_blks_read, toast_blks_hit, tidx_blks_read, tidx_blks_hit FROM pg_statio_user_tables"
	metrics:
	- datname:
		usage: "LABEL"
		description: "Name of current database"
	- schemaname:
		usage: "LABEL"
		description: "Name of the schema that this table is in"
	- relname:
		usage: "LABEL"
		description: "Name of this table"
	- heap_blks_read:
		usage: "COUNTER"
		description: "Number of disk blocks read from this table"
	- heap_blks_hit:
		usage: "COUNTER"
		description: "Number of buffer hits in this table"
	- idx_blks_read:
		usage: "COUNTER"
		description: "Number of disk blocks read from all indexes on this table"
	- idx_blks_hit:
		usage: "COUNTER"
		description: "Number of buffer hits in all indexes on this table"
	- toast_blks_read:
		usage: "COUNTER"
		description: "Number of disk blocks read from this table's TOAST table (if any)"
	- toast_blks_hit:
		usage: "COUNTER"
		description: "Number of buffer hits in this table's TOAST table (if any)"
	- tidx_blks_read:
		usage: "COUNTER"
		description: "Number of disk blocks read from this table's TOAST table indexes (if any)"
	- tidx_blks_hit:
		usage: "COUNTER"
		description: "Number of buffer hits in this table's TOAST table indexes (if any)"
	
	pg_wal_activity:
	query: "SELECT last_5_min_size_bytes,
		(SELECT COALESCE(sum(size),0) FROM pg_catalog.pg_ls_waldir()) AS total_size_bytes
		FROM (SELECT COALESCE(sum(size),0) AS last_5_min_size_bytes FROM pg_catalog.pg_ls_waldir() WHERE modification > CURRENT_TIMESTAMP - '5 minutes'::interval) x;"
	metrics:
	- last_5_min_size_bytes:
		usage: "GAUGE"
		description: "Current size in bytes of the last 5 minutes of WAL generation. Includes recycled WALs."
	- total_size_bytes:
		usage: "GAUGE"
		description: "Current size in bytes of the WAL directory"
	
	pg_database:
	query: "SELECT pg_database.datname, pg_database_size(pg_database.datname) as size FROM pg_database"
	metrics:
	- datname:
		usage: "LABEL"
		description: "Name of the database"
	- size:
		usage: "GAUGE"
		description: "Disk space used by the database"`

	if err := m.Create(ctx, cm); err != nil {
		return objs, fmt.Errorf("error while creating the new Postgres Exporter ConfigMap: %w", err)
	}
	m.log.Info("new Postgres Exporter ConfigMap created")

	return objs, nil
}

func (m *OperatorManager) deletePodEnvironmentConfigMap(ctx context.Context, namespace string) error {
	cm := &v1.ConfigMap{}
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

// toInstanceMatchingLabels makes the matching labels for the pods of the instances operated by the operator
func (m *OperatorManager) toInstanceMatchingLabels(namespace string) *client.MatchingLabels {
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
				return false, fmt.Errorf("error while fetching the operator pod with namespace %v and name %v: %w", ns, name, err)
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
		if _, err := m.InstallOrUpdateOperator(ctx, ns.Name); err != nil {
			return err
		}
	}

	m.log.Info("Done updating postgres operators in managed namespaces")
	return nil
}
