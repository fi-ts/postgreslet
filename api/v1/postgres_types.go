/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package v1

import (
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"

	"regexp"

	firewall "github.com/metal-stack/firewall-controller/v2/api/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	// UIDLabelName Name of the label referencing the owning Postgres resource uid in the control cluster
	UIDLabelName string = "postgres.database.fits.cloud/uid"
	// NameLabelName Name of the label referencing the owning Postgres resource name in the control cluster (which might not be unique)
	NameLabelName string = "postgres.database.fits.cloud/name"
	// TenantLabelName Name of the tenant label
	TenantLabelName string = "postgres.database.fits.cloud/tenant"
	// ProjectIDLabelName Name of the ProjectID label
	ProjectIDLabelName string = "postgres.database.fits.cloud/project-id"
	// ManagedByLabelName Name of the managed-by label
	ManagedByLabelName string = "postgres.database.fits.cloud/managed-by"
	// ManagedByLabelValue Value of the managed-by label
	ManagedByLabelValue string = "postgreslet"
	// PostgresFinalizerName Name of the finalizer to use
	PostgresFinalizerName string = "postgres.finalizers.database.fits.cloud"
	// CreatedByAnnotationKey is used to store who in person created this database
	CreatedByAnnotationKey string = "postgres.database.fits.cloud/created-by"
	// BackupConfigLabelName if set to true, this secret stores the backupConfig
	BackupConfigLabelName string = "postgres.database.fits.cloud/is-backup"
	// BackupConfigKey defines the key under which the BackupConfig is stored in the data map.
	BackupConfigKey = "config"
	// SharedBufferParameterKey defines the key under which the shared buffer size is stored in the parameters map. Defined by the postgres-operator/patroni
	SharedBufferParameterKey = "shared_buffers"
	// StandbyKey defines the key under which the standby configuration is stored in the CR.  Defined by the postgres-operator/patroni
	StandbyKey    = "standby"
	StandbyMethod = "streaming_host"
	// PartitionIDLabelName Name of the managed-by label
	PartitionIDLabelName string = "postgres.database.fits.cloud/partition-id"

	ApplicationLabelName             = "application"
	ApplicationLabelValue            = "spilo"
	SpiloRoleLabelName               = "spilo-role"
	SpiloRoleLabelValueMaster        = "master"
	SpiloRoleLabelValueStandbyLeader = "standby_leader"
	StatefulsetPodNameLabelName      = "statefulset.kubernetes.io/pod-name"
	ClusterNameLabelName             = "cluster-name"

	teamIDPrefix = "pg"

	DefaultPatroniParamValueLoopWait     uint32 = 10
	DefaultPatroniParamValueRetryTimeout uint32 = 10

	defaultPostgresParamValueTCPKeepAlivesIdle      = "200"
	defaultPostgresParamValueTCPKeepAlivesInterval  = "30"
	defaultPostgresParamValueLogFileMode            = "0600"
	defaultPostgresParamValueSSLMinProtocolVersion  = "TLSv1.2"
	defaultPostgresParamValueSSLPreferServerCiphers = "on"
	defaultPostgresParamValueSSLCiphers             = "ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384"
	defaultPostgresParamValueWalKeepSegments        = "64"
	defaultPostgresParamValueWalKeepSize            = "1GB"
	defaultPostgresParamValuePGStatStatementsMax    = "500"
	defaultSelectorDisableValue                     = "selector-disabled"
	defaultPostgresParamValuePasswordEncryption     = "scram-sha-256" // nolint
	defaultPostgresParamValueLogMinErrorStatement   = "WARNING"
	defaultPostgresParamValueLogErrorVerbosity      = "VERBOSE"
	defaultPostgresParamValueLogLinePrefix          = "%m [%p]: [%l-1] db=%d,user=%u,app=%a,client=%h "

	// PostgresAutoAssignedIPNamePrefix a prefix to add to the generated random name
	PostgresAutoAssignedIPNamePrefix = "pgaas-autoassign-"
	// PostgresAutoAssignedIPLabelKey tag to identify ips auto-assigned for a postgres
	PostgresAutoAssignedIPLabelKey = "postgres.database.fits.cloud/auto-assigned-ip"
	// PostgresAutoAssignedIPLabel tag to identify ips auto-assigned for a postgres
	PostgresAutoAssignedIPLabel = PostgresAutoAssignedIPLabelKey + "=true"

	PostresConfigSuperUsername        = "postgres"
	PostgresConfigReplicationUsername = "standby"
	PostgresConfigAuditorUsername     = "auditor"
	PostgresConfigMonitoringUsername  = "monitoring"

	zalando_timestamp_format = "2006-01-02T15:04:05-07:00"
)

var (
	ZalandoPostgresqlTypeMeta = metav1.TypeMeta{
		APIVersion: "acid.zalan.do/v1",
		Kind:       "postgresql",
	}
)

// BackupConfig defines all properties to configure backup of a database.
// This config is stored in the data section under the key BackupConfigKey as json payload.
type BackupConfig struct {
	// ID of this backupConfig
	ID string `json:"id"`
	// Name is a user defined description
	Name string `json:"name"`
	// ProjectID the project this backup is mapped to
	ProjectID string `json:"project"`
	// Tenant the tenant of the backup
	Tenant string `json:"tenant"`
	// CreatedBy is the name of the person or technical account which created this backupConfig
	CreatedBy string `json:"createdBy"`
	// Retention defines how many versions should be held in s3
	Retention string `json:"retention"`
	// Schedule in cron syntax when to run the backup periodically
	Schedule string `json:"schedule"`

	// S3Endpoint the url of the s3 endpoint
	S3Endpoint string `json:"s3endpoint"`
	// S3BucketName is the name of the bucket where the backup should be stored.
	S3BucketName string `json:"s3bucketname"`
	// S3Region the region of the aws s3
	S3Region string `json:"s3region"`
	// S3AccessKey is the accesskey which must have write access
	S3AccessKey string `json:"s3accesskey"`
	// S3SecretKey is the secretkey which must match to the accesskey
	S3SecretKey string `json:"s3secretkey"`
	// S3EncryptionKey if set, server side s3 encryption is used.
	S3EncryptionKey *string `json:"s3encryptionkey,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenant`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Replicas",type=string,JSONPath=`.spec.numberOfInstances`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.socket.ip`
// +kubebuilder:printcolumn:name="Port",type=integer,JSONPath=`.status.socket.port`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.description`

// Postgres is the Schema for the postgres API
type Postgres struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresSpec   `json:"spec,omitempty"`
	Status PostgresStatus `json:"status,omitempty"`
}

// PostgresSpec defines the desired state of Postgres
type PostgresSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Description
	Description string `json:"description,omitempty"`
	// ProjectID metal project ID
	ProjectID string `json:"projectID,omitempty"`
	// Tenant metal tenant
	Tenant string `json:"tenant,omitempty"`
	// PartitionID the partition where the database is created
	PartitionID string `json:"partitionID,omitempty"`

	// NumberOfInstances number of replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	NumberOfInstances int32 `json:"numberOfInstances,omitempty"`

	// Version is the version of Postgre-as-a-Service
	Version string `json:"version,omitempty"`

	// Size of the database
	Size *Size `json:"size,omitempty"`

	// todo: add default
	// Maintenance defines automatic maintenance of the database
	Maintenance []string `json:"maintenance,omitempty"`

	// AccessList defines access restrictions
	AccessList *AccessList `json:"accessList,omitempty"`

	// BackupSecretRef reference to the secret where the backup credentials are stored
	BackupSecretRef string `json:"backupSecretRef,omitempty"`

	// PostgresRestore
	PostgresRestore *PostgresRestore `json:"restore,omitempty"`

	// PostgresConnection Connection info of a streaming host, independent of the current role (leader or standby)
	PostgresConnection *PostgresConnection `json:"connection,omitempty"`

	// AuditLogs enable or disable default audit logs
	AuditLogs *bool `json:"auditLogs,omitempty"`

	// PostgresParams additional parameters that are passed along to the postgres config
	PostgresParams map[string]string `json:"postgresParams,omitempty"`

	// DedicatedLoadBalancerIP The ip to use for the load balancer
	DedicatedLoadBalancerIP *string `json:"dedicatedLoadBalancerIP,omitempty"`

	// DedicatedLoadBalancerPort The port to use for the load balancer
	DedicatedLoadBalancerPort *int32 `json:"dedicatedLoadBalancerPort,omitempty"`

	// DisableLoadBalancers enable or disable the Load Balancers (Services)
	DisableLoadBalancers *bool `json:"disableLoadBalancers,omitempty"`
}

// AccessList defines the type of restrictions to access the database
type AccessList struct {
	// SourceRanges defines a list of prefixes in CIDR Notation e.g. 1.2.3.0/24 or fdaa::/104
	SourceRanges []string `json:"sourceRanges,omitempty"`
}

// Todo: Add defaults
// Size defines the size aspects of the database
type Size struct {
	// CPU is in the format as pod.spec.resource.request.cpu
	CPU string `json:"cpu,omitempty"`
	// Memory is in the format as pod.spec.resource.request.memory
	Memory string `json:"memory,omitempty"`
	// SharedBuffer of the database
	SharedBuffer string `json:"sharedBuffer,omitempty"`
	// Memoryfactor used to calculate the memory
	MemoryFactor uint8 `json:"memoryfactor,omitempty"`

	// StorageSize the amount of Storage this database will get
	// +kubebuilder:default="1Gi"
	// +kubebuilder:validation:Pattern=^[1-9][0-9]*Gi
	StorageSize string `json:"storageSize,omitempty"`
}

// Restore defines what to restore from where
type PostgresRestore struct {
	// SourcePostgresID internal ID of the Postgres instance to whose backup to restore
	SourcePostgresID string `json:"postgresID,omitempty"`
	// Timestamp The point in time to recover. Must be set, or the clone with switch from WALs from the S3 to a basebackup via direct sql connection (which won't work when the source db is managed by another posgres-operator)
	Timestamp string `json:"timestamp,omitempty"`
}

// PostgresStatus defines the observed state of Postgres
type PostgresStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Description string `json:"description,omitempty"`

	Socket Socket `json:"socket,omitempty"`

	AdditionalSockets []Socket `json:"additionalSockets,omitempty"`

	ChildName string `json:"childName,omitempty"`
}

// Socket represents load-balancer socket of Postgres
type Socket struct {
	IP   string `json:"ip,omitempty"`
	Port int32  `json:"port,omitempty"`
}

// +kubebuilder:object:root=true

// PostgresList contains a list of Postgres
type PostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Postgres `json:"items"`
}

// PostgresConnection A remote postgres instance this one is linked to, e.g. for standby purposes.
type PostgresConnection struct {
	// ConnectedPostgresID internal ID of the connected Postgres instance
	ConnectedPostgresID string `json:"postgresID,omitempty"`
	// ConnectionSecretName name of the internal secret used to connect to the remote postgres
	ConnectionSecretName string `json:"secretName,omitempty"`
	// ConnectionIP IP of the remote postgres
	ConnectionIP string `json:"ip,omitempty"`
	// ConnectionPort port of the remote postgres
	ConnectionPort uint16 `json:"port,omitempty"`
	// SynchronousReplication determines if async  or sync replication is used for the standby postgres
	SynchronousReplication bool `json:"synchronous,omitempty"`
	// ReplicationPrimary determines if THIS side of the connection is the primary or the standby side
	ReplicationPrimary bool `json:"localSideIsPrimary,omitempty"`
}

var SvcLoadBalancerLabel = map[string]string{
	ManagedByLabelName: ManagedByLabelValue,
}

var alphaNumericRegExp *regexp.Regexp = regexp.MustCompile("[^a-zA-Z0-9]+")

// HasSourceRanges returns true if SourceRanges are set
func (p *Postgres) HasSourceRanges() bool {
	return p.Spec.AccessList != nil && p.Spec.AccessList.SourceRanges != nil
}

// IsBeingDeleted returns true if the deletion-timestamp is set
func (p *Postgres) IsBeingDeleted() bool {
	return !p.ObjectMeta.DeletionTimestamp.IsZero()
}

// ToCWNP returns CRD ClusterwideNetworkPolicy derived from CRD Postgres
func (p *Postgres) ToCWNP(port int) (*firewall.ClusterwideNetworkPolicy, error) {
	portObj := intstr.FromInt(port)
	tcp := corev1.ProtocolTCP
	ports := []networkingv1.NetworkPolicyPort{
		{Port: &portObj, Protocol: &tcp},
	}

	// When SourceRanges of Postgres aren't set, ipblocks will be left blank,
	// which implies denying all accecces.
	ipblocks := []networkingv1.IPBlock{}
	if p.HasSourceRanges() {
		for _, src := range p.Spec.AccessList.SourceRanges {
			parsedSrc, err := netip.ParsePrefix(src)
			if err != nil {
				return nil, fmt.Errorf("unable to parse source range %s: %w", src, err)
			}
			ipblock := networkingv1.IPBlock{
				CIDR: parsedSrc.String(),
			}
			ipblocks = append(ipblocks, ipblock)
		}
	} else {
		// add an empty block so access is blocked
		ipblock := networkingv1.IPBlock{}
		ipblocks = append(ipblocks, ipblock)
	}

	policy := &firewall.ClusterwideNetworkPolicy{}
	// todo: Use the exported const.
	policy.Namespace = firewall.ClusterwideNetworkPolicyNamespace
	policy.Name = p.ToPeripheralResourceName()
	policy.Spec.Ingress = []firewall.IngressRule{
		{Ports: ports, From: ipblocks},
	}

	return policy, nil
}

func (p *Postgres) ToKey() *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: p.Namespace,
		Name:      p.Name,
	}
}

func (p *Postgres) ToSharedSvcLB(lbIP string, lbPort int32, enableStandbyLeaderSelector bool, enableLegacyStandbySelector bool, standbyClustersSourceRanges []string) *corev1.Service {
	lb := &corev1.Service{}
	lb.Spec.Type = "LoadBalancer"

	lb.Annotations = map[string]string{
		"metallb.universe.tf/allow-shared-ip": "spilo",
	}

	lb.Namespace = p.ToPeripheralResourceNamespace()
	lb.Name = p.ToSharedSvcLBName()
	lb.SetLabels(SvcLoadBalancerLabel)

	lbsr := []string{}
	if p.HasSourceRanges() {
		lbsr = append(lbsr, p.Spec.AccessList.SourceRanges...)
	}
	lbsr = append(lbsr, standbyClustersSourceRanges...)
	if len(lbsr) == 0 {
		// block by default
		lbsr = append(lbsr, "255.255.255.255/32")
	}
	lb.Spec.LoadBalancerSourceRanges = lbsr

	port := corev1.ServicePort{}
	port.Name = "postgresql"
	port.Port = lbPort
	port.Protocol = corev1.ProtocolTCP
	port.TargetPort = intstr.FromInt(5432)
	lb.Spec.Ports = []corev1.ServicePort{port}

	lb.Spec.Selector = map[string]string{
		ApplicationLabelName: ApplicationLabelValue,
		ClusterNameLabelName: p.ToPeripheralResourceName(),
		"team":               p.generateTeamID(),
	}
	if p.IsReplicationPrimaryOrStandalone() {
		lb.Spec.Selector[SpiloRoleLabelName] = SpiloRoleLabelValueMaster
	} else {
		if enableStandbyLeaderSelector {
			// Only set this value when we are NOT a primary and the StandbyLeaderSelector is enabled.
			lb.Spec.Selector[SpiloRoleLabelName] = SpiloRoleLabelValueStandbyLeader
		} else if enableLegacyStandbySelector {
			lb.Spec.Selector[SpiloRoleLabelName] = SpiloRoleLabelValueMaster
		} else {
			// select the first pod in the statefulset
			lb.Spec.Selector[StatefulsetPodNameLabelName] = p.ToPeripheralResourceName() + "-0"
		}
	}
	if p.DisableLoadBalancers() {
		lb.Spec.Selector[ClusterNameLabelName] = defaultSelectorDisableValue
	}

	if len(lbIP) > 0 {
		// if no ip is set, a new loadbalancer will be created automatically
		lb.Spec.LoadBalancerIP = lbIP
	}

	return lb
}

// ToSharedSvcLBName returns the name of the peripheral resource Service LoadBalancer.
// It's different from all other peripheral resources because the operator
// already generates one service with that name.
func (p *Postgres) ToSharedSvcLBName() string {
	return p.ToPeripheralResourceName() + "-external"
}

func (p *Postgres) ToSharedSvcLBNamespacedName() *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: p.ToPeripheralResourceNamespace(),
		Name:      p.ToSharedSvcLBName(),
	}
}

func (p *Postgres) EnableSharedSVCLB(enableForceSharedIP bool) bool {
	if enableForceSharedIP {
		// shared IP is forced, so force it. No more questions asked.
		return true
	}

	if p.Spec.DedicatedLoadBalancerIP == nil {
		// No dedicated ip set at all, enabled shared lb
		return true
	}

	if *p.Spec.DedicatedLoadBalancerIP == "" {
		// Empty IP set, enable shared lb
		return true
	}

	return false
}

func (p *Postgres) ToDedicatedSvcLB(lbIP string, lbPort int32, standbyClustersSourceRanges []string, sharedSvcLbAlsoEnabled bool) *corev1.Service {
	lb := &corev1.Service{}
	lb.Spec.Type = "LoadBalancer"

	lb.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeLocal

	lb.Namespace = p.ToPeripheralResourceNamespace()
	lb.Name = p.ToDedicatedSvcLBName()
	lb.SetLabels(SvcLoadBalancerLabel)

	lb.Annotations = map[string]string{}

	lbsr := []string{}
	if p.HasSourceRanges() {
		lbsr = append(lbsr, p.Spec.AccessList.SourceRanges...)
	}
	lbsr = append(lbsr, standbyClustersSourceRanges...)
	if len(lbsr) == 0 {
		// block by default
		lbsr = append(lbsr, "255.255.255.255/32")
	}
	lb.Spec.LoadBalancerSourceRanges = lbsr

	port := corev1.ServicePort{}
	port.Name = "postgresql"
	port.Port = lbPort
	port.Protocol = corev1.ProtocolTCP
	port.TargetPort = intstr.FromInt(5432)
	lb.Spec.Ports = []corev1.ServicePort{port}

	lb.Spec.Selector = map[string]string{
		ApplicationLabelName: ApplicationLabelValue,
		ClusterNameLabelName: p.ToPeripheralResourceName(),
		"team":               p.generateTeamID(),
	}
	if p.IsReplicationPrimaryOrStandalone() {
		lb.Spec.Selector[SpiloRoleLabelName] = SpiloRoleLabelValueMaster
	} else {
		// select the first pod in the statefulset
		lb.Spec.Selector[StatefulsetPodNameLabelName] = p.ToPeripheralResourceName() + "-0"
	}
	if p.DisableLoadBalancers() {
		lb.Spec.Selector[ClusterNameLabelName] = defaultSelectorDisableValue
	}

	if len(lbIP) > 0 {
		lb.Spec.LoadBalancerIP = lbIP
	}

	return lb
}

// ToSharedSvcLBName returns the name of the peripheral resource Service LoadBalancer.
// It's different from all other peripheral resources because the operator
// already generates one service with that name.
func (p *Postgres) ToDedicatedSvcLBName() string {
	return p.ToPeripheralResourceName() + "-dedicated"
}

func (p *Postgres) ToDedicatedSvcLBNamespacedName() *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: p.ToPeripheralResourceNamespace(),
		Name:      p.ToDedicatedSvcLBName(),
	}
}

func (p *Postgres) EnableDedicatedSVCLB() bool {
	if p.Spec.DedicatedLoadBalancerIP == nil {
		// No dedicated ip set at all, disable dedicated lb
		return false
	}

	if *p.Spec.DedicatedLoadBalancerIP == "" {
		// Empty IP set, disable dedicated lb
		return false
	}

	return true
}

func (p *Postgres) ToPeripheralResourceName() string {

	return p.generateTeamID() + "-" + p.generateDatabaseName()
}

// ToUserPasswordsSecret returns the secret containing user password pairs
func (p *Postgres) ToUserPasswordsSecret(src *corev1.SecretList, scheme *runtime.Scheme) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	secret.Namespace = p.Namespace
	secret.Name = p.ToUserPasswordsSecretName()
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{}

	// Fill in the contents of the new secret
	for _, v := range src.Items {
		secret.Data[string(v.Data["username"])] = v.Data["password"]
	}

	return secret, nil
}

// ToUserPasswordsSecretName returns the name of the secret containing user password pairs
func (p *Postgres) ToUserPasswordsSecretName() string {
	return p.Name + "-passwords"
}

// ToBackupSecretName returns the name of the secret containing backup credentials
func (p *Postgres) ToBackupSecretName() string {
	return p.Spec.ProjectID + "-backup"
}

// ToUserPasswordsSecretListOption returns the argument for listing secrets
func (p *Postgres) ToUserPasswordsSecretListOption() []client.ListOption {
	return []client.ListOption{
		client.InNamespace(p.ToPeripheralResourceNamespace()),
		p.ToZalandoPostgresqlMatchingLabels(),
	}
}

func (p *Postgres) ToUserPasswordSecretMatchingLabels() map[string]string {
	return map[string]string{
		ApplicationLabelName: ApplicationLabelValue,
		"cluster-name":       p.ToPeripheralResourceName(),
		"team":               p.generateTeamID(),
	}
}

func (p *Postgres) generateTeamID() string {
	// We only want letters and numbers
	generatedTeamID := alphaNumericRegExp.ReplaceAllString(p.Spec.Tenant, "")

	// Add prefix to make sure the string is a valid dns entry (aka does not start with a number).
	// Also acts as minimal teamID in case the Tenant does not contain any alphanumeric characters.
	generatedTeamID = teamIDPrefix + generatedTeamID

	// and only lower case
	generatedTeamID = strings.ToLower(generatedTeamID)

	// Limit size
	maxLen := 16
	if len(generatedTeamID) > maxLen {
		generatedTeamID = generatedTeamID[:maxLen]
	}

	return generatedTeamID
}

func (p *Postgres) generateDatabaseName() string {
	// We only want letters and numbers
	generatedDatabaseName := alphaNumericRegExp.ReplaceAllString(string(p.Spec.Description), "")

	// and only lower case
	generatedDatabaseName = strings.ToLower(generatedDatabaseName)

	// Limit the length of the description part of the name
	maxLen := 15
	if len(generatedDatabaseName) > maxLen {
		generatedDatabaseName = generatedDatabaseName[:maxLen]
	}

	// Add UID in the mix
	generatedDatabaseName += alphaNumericRegExp.ReplaceAllString(string(p.Name), "")

	// Limit to final size
	// This way, we have at least 5 chars of the uid as part of the database name.
	maxLen = 20
	if len(generatedDatabaseName) > maxLen {
		generatedDatabaseName = generatedDatabaseName[:maxLen]
	}

	return generatedDatabaseName
}

func (p *Postgres) ToPeripheralResourceNamespace() string {
	// We only want letters and numbers
	projectID := alphaNumericRegExp.ReplaceAllString(p.Spec.ProjectID, "")

	// Limit size
	maxLen := 16
	if len(projectID) > maxLen {
		projectID = projectID[:maxLen]
	}

	// We only want letters and numbers
	name := alphaNumericRegExp.ReplaceAllString(string(p.Name), "")

	// Limit size
	maxLen = 20
	if len(name) > maxLen {
		name = name[:maxLen]
	}

	return projectID + "-" + name
}

func (p *Postgres) ToDNSName(tlsSubDomain string) string {
	// We only want letters and numbers
	name := alphaNumericRegExp.ReplaceAllString(string(p.Name), "")
	// Limit size
	maxLen := 12
	if len(name) > maxLen {
		name = name[:maxLen]
	}
	return name + "." + tlsSubDomain
}

func (p *Postgres) ToTLSSecretName() string {
	return "pg-tls"
}

func (p *Postgres) ToPeripheralResourceLookupKey() types.NamespacedName {
	return types.NamespacedName{
		Namespace: p.ToPeripheralResourceNamespace(),
		Name:      p.ToPeripheralResourceName(),
	}
}

func (p *Postgres) ToUnstructuredZalandoPostgresql(z *zalando.Postgresql, c *corev1.ConfigMap, sc string, pgParamBlockList map[string]bool, rbs *BackupConfig, srcDB *Postgres, patroniTTL, patroniLoopWait, patroniRetryTimeout uint32, dboIsSuperuser bool, enableTlsCert bool, image string) (*unstructured.Unstructured, error) {
	if z == nil {
		z = &zalando.Postgresql{}
	}
	z.TypeMeta = ZalandoPostgresqlTypeMeta
	z.Namespace = p.ToPeripheralResourceNamespace()
	z.Name = p.ToPeripheralResourceName()
	z.Labels = p.ToZalandoPostgresqlMatchingLabels()
	// Add the newly introduced label only here, not in  p.ToZalandoPostgresqlMatchingLabels() (so that the selectors using  p.ToZalandoPostgresqlMatchingLabels() will still work until all postgres resources have that new label)
	// TODO once all the custom resources have that new label, move this part to p.ToZalandoPostgresqlMatchingLabels()
	z.Labels[PartitionIDLabelName] = p.Spec.PartitionID

	if image != "" {
		z.Spec.DockerImage = image
	}
	z.Spec.NumberOfInstances = p.Spec.NumberOfInstances
	z.Spec.PostgresqlParam.PgVersion = p.Spec.Version

	// initialize the parameters
	z.Spec.PostgresqlParam.Parameters = map[string]string{}
	// enable default audit logs (if not configured otherwise)
	if p.Spec.AuditLogs == nil || *p.Spec.AuditLogs {
		enableAuditLogs(z.Spec.PostgresqlParam.Parameters)
	}
	// set some default postgres parameters
	setDefaultPostgresParams(z.Spec.PostgresqlParam.Parameters, p.Spec.Version)
	// now set the given generic parameters (and potentially allow overwriting of default postgres params or audit log params)
	setPostgresParams(z.Spec.PostgresqlParam.Parameters, p.Spec.PostgresParams, pgParamBlockList)
	// finally, overwrite the (special to us) shared buffer parameter
	setSharedBufferSize(z.Spec.PostgresqlParam.Parameters, p.Spec.Size.SharedBuffer)

	z.Spec.Resources = &zalando.Resources{}
	z.Spec.Resources.ResourceRequests.CPU = ptr.To(p.Spec.Size.CPU)
	z.Spec.Resources.ResourceRequests.Memory = ptr.To(p.Spec.Size.Memory)
	z.Spec.Resources.ResourceLimits.CPU = ptr.To(p.Spec.Size.CPU)
	z.Spec.Resources.ResourceLimits.Memory = ptr.To(p.Spec.Size.Memory)
	z.Spec.TeamID = p.generateTeamID()
	z.Spec.Volume.Size = p.Spec.Size.StorageSize
	z.Spec.Volume.StorageClass = sc

	z.Spec.Patroni.TTL = patroniTTL
	z.Spec.Patroni.LoopWait = patroniLoopWait
	z.Spec.Patroni.RetryTimeout = patroniRetryTimeout
	z.Spec.Patroni.SynchronousMode = true
	z.Spec.Patroni.SynchronousModeStrict = false

	// required with image ermajn/postgres-operator:v1.6.0-20-g1cc71663-dirty
	// see https://github.com/fi-ts/postgreslet/issues/293
	z.Spec.EnableConnectionPooler = ptr.To(false)

	prefix := alphaNumericRegExp.ReplaceAllString(string(p.Spec.Tenant), "")
	prefix = strings.ToLower(prefix)
	databaseName := prefix + "db01"
	prepDbName := prefix + "prepdb01"
	ownerName := prefix + "dbo"

	// Create database owner
	z.Spec.Users = make(map[string]zalando.UserFlags)
	z.Spec.Users[ownerName] = zalando.UserFlags{"createdb", "createrole"}
	if dboIsSuperuser {
		z.Spec.Users[ownerName] = zalando.UserFlags{"createdb", "createrole", "superuser"}
	}
	// Add auditor user
	z.Spec.Users[PostgresConfigAuditorUsername] = zalando.UserFlags{"nologin"}
	// Add monitoring user
	z.Spec.Users[PostgresConfigMonitoringUsername] = zalando.UserFlags{"login"}

	// Create default database
	z.Spec.Databases = make(map[string]string)
	z.Spec.Databases[databaseName] = ownerName

	// Create prepared database
	z.Spec.PreparedDatabases = make(map[string]zalando.PreparedDatabase)
	z.Spec.PreparedDatabases[prepDbName] = zalando.PreparedDatabase{
		DefaultUsers: true,
		Extensions: map[string]string{
			"pg_partman": "public",
			"pgcrypto":   "public",
		},
		PreparedSchemas: map[string]zalando.PreparedSchema{
			"data":    {},
			"history": {},
		},
	}

	// skip if the configmap does not exist
	if c != nil {
		z.Spec.AdditionalVolumes = p.buildAdditionalVolumes(c)
		z.Spec.Sidecars = p.buildSidecars(c)
	}

	if p.HasSourceRanges() {
		z.Spec.AllowedSourceRanges = p.Spec.AccessList.SourceRanges
	}

	if p.Spec.PostgresRestore != nil && rbs != nil && srcDB != nil {
		// make sure there is always a value set. The operator will fall back to CLONE_WITH_BASEBACKUP, which assumes the source db's credentials are existing within the same namespace, which is not the case with the postgreslet.
		if p.Spec.PostgresRestore.Timestamp == "" {
			// e.g. 2021-12-07T15:28:00+01:00
			p.Spec.PostgresRestore.Timestamp = time.Now().Format(zalando_timestamp_format)
		}

		z.Spec.Clone = &zalando.CloneDescription{
			ClusterName:       srcDB.ToPeripheralResourceName(),
			EndTimestamp:      p.Spec.PostgresRestore.Timestamp,
			S3Endpoint:        rbs.S3Endpoint,
			S3AccessKeyId:     rbs.S3AccessKey,
			S3SecretAccessKey: rbs.S3SecretKey,
			S3ForcePathStyle:  ptr.To(true),
		}
	} else {
		// if we don't set the clone block, remove it completely
		z.Spec.Clone = nil
	}

	// Enable replication (using unstructured json)
	if p.IsReplicationPrimaryOrStandalone() {
		// delete field
		z.Spec.StandbyCluster = nil
	} else {
		// overwrite connection info
		z.Spec.StandbyCluster = &zalando.StandbyDescription{
			StandbyHost: p.Spec.PostgresConnection.ConnectionIP,
			StandbyPort: strconv.FormatInt(int64(p.Spec.PostgresConnection.ConnectionPort), 10),
			// S3WalPath:              "",
		}
	}

	if enableTlsCert {
		z.Spec.TLS = &zalando.TLSDescription{
			SecretName: p.ToTLSSecretName(),
		}
	} else {
		z.Spec.TLS = nil
	}

	jsonZ, err := runtime.DefaultUnstructuredConverter.ToUnstructured(z)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
	}
	jsonSpec, _ := jsonZ["spec"].(map[string]interface{})

	// In the code, zalando's `MaintenanceWindows` is a `struct`, but in the CRD
	// it's an array of strings, so we can only set its `Unstructured` contents
	// and deal with possible `nil`.
	jsonSpec["maintenanceWindows"] = p.Spec.Maintenance
	deleteIfEmpty(jsonSpec, "maintenanceWindows")

	// Delete unused fields
	deleteIfEmpty(jsonSpec, "clone")
	deleteIfEmpty(jsonSpec, "patroni") // if in use, deleteIfEmpty needs to consider the case of struct.
	deleteIfEmpty(jsonSpec, "podAnnotations")
	deleteIfEmpty(jsonSpec, "serviceAnnotations")
	deleteIfEmpty(jsonSpec, "standby")
	deleteIfEmpty(jsonSpec, "tls")
	deleteIfEmpty(jsonSpec, "users")

	jsonP, _ := jsonSpec["postgresql"].(map[string]interface{})
	deleteIfEmpty(jsonP, "parameters")

	return &unstructured.Unstructured{
		Object: jsonZ,
	}, nil
}

func (p *Postgres) ToZalandoPostgresqlMatchingLabels() client.MatchingLabels {
	return client.MatchingLabels{
		ProjectIDLabelName: p.Spec.ProjectID,
		TenantLabelName:    p.Spec.Tenant,
		UIDLabelName:       string(p.UID),
		NameLabelName:      p.Name,
	}
}

func (p *Postgres) HasFinalizer(finalizerName string) bool {
	return containsElem(p.ObjectMeta.Finalizers, finalizerName)
}

func (p *Postgres) AddFinalizer(finalizerName string) {
	p.ObjectMeta.Finalizers = append(p.ObjectMeta.Finalizers, finalizerName)
}

func (p *Postgres) RemoveFinalizer(finalizerName string) {
	p.ObjectMeta.Finalizers = removeElem(p.ObjectMeta.Finalizers, finalizerName)
}

func containsElem(ss []string, s string) bool {
	for _, elem := range ss {
		if elem == s {
			return true
		}
	}
	return false
}

func removeElem(ss []string, s string) (out []string) {
	for _, elem := range ss {
		if elem == s {
			continue
		}
		out = append(out, elem)
	}
	return
}

func deleteIfEmpty(json map[string]interface{}, key string) {
	i := json[key]

	// interface has type and value. The chained function calls deal with nil value with type.
	if i == nil || reflect.ValueOf(json[key]).IsNil() {
		delete(json, key)
	}
}

func init() {
	SchemeBuilder.Register(&Postgres{}, &PostgresList{})
}

func (p *Postgres) buildAdditionalVolumes(c *corev1.ConfigMap) []zalando.AdditionalVolume {
	if c == nil {
		// abort if the global configmap is not there
		return nil
	}

	// Unmarshal yaml-string of additional volumes
	volumes := []zalando.AdditionalVolume{}
	if err := yaml.Unmarshal([]byte(c.Data["additional-volumes"]), &volumes); err != nil {
		return nil
	}

	return volumes
}

func (p *Postgres) buildSidecars(c *corev1.ConfigMap) []zalando.Sidecar {
	if c == nil {
		// abort if the global configmap is not there
		return nil
	}

	// Unmarshal yaml-string of exporter
	sidecars := []zalando.Sidecar{}
	if err := yaml.Unmarshal([]byte(c.Data["sidecars"]), &sidecars); err != nil {
		return nil
	}

	// Deal with dynamically assigned name
	for i := range sidecars {
		for j := range sidecars[i].Env {
			if sidecars[i].Env[j].ValueFrom != nil && sidecars[i].Env[j].ValueFrom.SecretKeyRef != nil {
				sidecars[i].Env[j].ValueFrom.SecretKeyRef.Name = PostgresConfigMonitoringUsername + "." + p.ToPeripheralResourceName() + ".credentials"
				break
			}
		}
	}

	return sidecars
}

// setSharedBufferSize converts and, if valid, sets the shared_buffer parameter in the given map.
func setSharedBufferSize(parameters map[string]string, shmSize string) {
	// First step is to convert the string back to a quantity
	size, err := resource.ParseQuantity(shmSize)
	if err == nil {
		// if successful, get the given shared buffer size in bytes.
		sizeInBytes, ok := size.AsInt64()
		if ok && sizeInBytes >= (32*1024*1024) {
			// if more than 32Mi (our minimum value), convert the value to MB as required by postgres (although the docs are not very specific about that)
			sizeInMB := sizeInBytes / (1024 * 1024)
			parameters[SharedBufferParameterKey] = strconv.FormatInt(sizeInMB, 10) + "MB"
		}
	}
}

func (p *Postgres) IsReplicationPrimaryOrStandalone() bool {
	if p.Spec.PostgresConnection == nil || p.Spec.PostgresConnection.ReplicationPrimary {
		// nothing is configured, or we are the leader. nothing to do.
		return true
	}
	return false
}

func (p *Postgres) IsReplicationTarget() bool {
	if p.Spec.PostgresConnection != nil && !p.Spec.PostgresConnection.ReplicationPrimary {
		// sth is configured and we are not the leader
		return true
	}
	return false
}

// enableAuditLogs configures this postgres instances audit logging
func enableAuditLogs(parameters map[string]string) {
	// default values: bg_mon,pg_stat_statements,pgextwlist,pg_auth_mon,set_user,timescaledb,pg_cron,pg_stat_kcache
	parameters["shared_preload_libraries"] = "pg_stat_statements,pgextwlist,pg_auth_mon,set_user,timescaledb,pg_cron,pg_stat_kcache,pgaudit"
	parameters["pgaudit.log_catalog"] = "off"
	parameters["pgaudit.log"] = "ddl,role"
	parameters["pgaudit.log_relation"] = "on"
	parameters["pgaudit.log_parameter"] = "on"
	parameters["log_statement"] = "none"
}

// setDefaultPostgresParams configures default keepalive values
func setDefaultPostgresParams(parameters map[string]string, version string) {
	// set default parameters
	parameters["log_error_verbosity"] = defaultPostgresParamValueLogErrorVerbosity
	parameters["log_file_mode"] = defaultPostgresParamValueLogFileMode
	parameters["log_line_prefix"] = defaultPostgresParamValueLogLinePrefix
	parameters["log_min_error_statement"] = defaultPostgresParamValueLogMinErrorStatement
	parameters["password_encryption"] = defaultPostgresParamValuePasswordEncryption
	parameters["pg_stat_statements.max"] = defaultPostgresParamValuePGStatStatementsMax
	parameters["ssl_ciphers"] = defaultPostgresParamValueSSLCiphers
	parameters["ssl_prefer_server_ciphers"] = defaultPostgresParamValueSSLPreferServerCiphers
	parameters["tcp_keepalives_idle"] = defaultPostgresParamValueTCPKeepAlivesIdle
	parameters["tcp_keepalives_interval"] = defaultPostgresParamValueTCPKeepAlivesInterval

	// set version specific parameters
	v, err := strconv.Atoi(version)
	if err != nil {
		return
	}
	// Postgres 12 and up
	if v >= 12 {
		parameters["ssl_min_protocol_version"] = defaultPostgresParamValueSSLMinProtocolVersion
	}
	// Postgres 13 and up
	if v >= 13 {
		parameters["wal_keep_size"] = defaultPostgresParamValueWalKeepSize
	} else {
		parameters["wal_keep_segments"] = defaultPostgresParamValueWalKeepSegments
	}
}

// setPostgresParams add the provided params to the parameter map (but ignore params that are blocked)
func setPostgresParams(parameters map[string]string, providedParams map[string]string, blockList map[string]bool) {
	for k, v := range providedParams {
		if _, isBlocked := blockList[k]; isBlocked {
			// k is on the blockList, ignore that param
			continue
		}
		parameters[k] = v
	}
}

func (p *Postgres) ToStandbyClusterIngresCWNPName() string {
	return p.ToPeripheralResourceName() + "-standby-ingress"
}
func (p *Postgres) ToStandbyClusterEgresCWNPName() string {
	return p.ToPeripheralResourceName() + "-standby-egress"
}

func (p *Postgres) ToStandbyClusterIngressCWNP(sourceCIDRs []string) (*firewall.ClusterwideNetworkPolicy, error) {

	standbyIngressCWNPName := p.ToStandbyClusterIngresCWNPName()
	standbyIngressCWNP := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyIngressCWNPName, Namespace: firewall.ClusterwideNetworkPolicyNamespace}}

	//
	// Create ingress rule from pre-configured CIDR to this DBs port
	//
	standbyClusterIngressIPBlocks := []networkingv1.IPBlock{}
	for _, cidr := range sourceCIDRs {
		remoteServiceClusterCIDR, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("unable to parse standby host ip %s: %w", p.Spec.PostgresConnection.ConnectionIP, err)
		}
		standbyClusterIPs := networkingv1.IPBlock{
			CIDR: remoteServiceClusterCIDR.String(),
		}
		standbyClusterIngressIPBlocks = append(standbyClusterIngressIPBlocks, standbyClusterIPs)
	}

	// Add Port to CWNP (if known)
	tcp := corev1.ProtocolTCP
	ingressTargetPorts := []networkingv1.NetworkPolicyPort{}
	if p.Status.Socket.Port != 0 {
		portObj := intstr.FromInt(int(p.Status.Socket.Port))
		ingressTargetPorts = append(ingressTargetPorts, networkingv1.NetworkPolicyPort{Port: &portObj, Protocol: &tcp})
	}
	standbyIngressCWNP.Spec.Ingress = []firewall.IngressRule{
		{Ports: ingressTargetPorts, From: standbyClusterIngressIPBlocks},
	}

	return standbyIngressCWNP, nil
}

func (p *Postgres) ToStandbyClusterEgressCWNP() (*firewall.ClusterwideNetworkPolicy, error) {
	standbyClusterEgressIPBlocks := []networkingv1.IPBlock{}
	if p.Spec.PostgresConnection.ConnectionIP != "" {
		remoteServiceClusterCIDR, err := netip.ParsePrefix(p.Spec.PostgresConnection.ConnectionIP + "/32")
		if err != nil {
			return nil, fmt.Errorf("unable to parse standby host ip %s: %w", p.Spec.PostgresConnection.ConnectionIP, err)
		}
		standbyClusterIPs := networkingv1.IPBlock{
			CIDR: remoteServiceClusterCIDR.String(),
		}
		standbyClusterEgressIPBlocks = append(standbyClusterEgressIPBlocks, standbyClusterIPs)
	}

	tcp := corev1.ProtocolTCP
	standbyEgressCWNPName := p.ToStandbyClusterEgresCWNPName()
	standbyEgressCWNP := &firewall.ClusterwideNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: standbyEgressCWNPName, Namespace: firewall.ClusterwideNetworkPolicyNamespace}}
	// Add Port to CWNP
	egressTargetPorts := []networkingv1.NetworkPolicyPort{}
	if p.Spec.PostgresConnection.ConnectionPort != 0 {
		portObj := intstr.FromInt(int(p.Spec.PostgresConnection.ConnectionPort))
		egressTargetPorts = append(egressTargetPorts, networkingv1.NetworkPolicyPort{Port: &portObj, Protocol: &tcp})
	}
	standbyEgressCWNP.Spec.Egress = []firewall.EgressRule{
		{Ports: egressTargetPorts, To: standbyClusterEgressIPBlocks},
	}

	return standbyEgressCWNP, nil
}

func (p *Postgres) DisableLoadBalancers() bool {
	if p.Spec.DisableLoadBalancers == nil {
		return false
	}

	return *p.Spec.DisableLoadBalancers
}
