/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package v1

import (
	"fmt"

	"regexp"

	firewall "github.com/metal-stack/firewall-controller/api/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	"inet.af/netaddr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.description`
// +kubebuilder:printcolumn:name="Load-Balancer-IP",type=string,JSONPath=`.status.socket.ip`
// +kubebuilder:printcolumn:name="Load-Balancer-Port",type=integer,JSONPath=`.status.socket.port`

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
	// +kubebuilder:validation:Enum="12";
	// +kubebuilder:default="12"
	Version string `json:"version,omitempty"`

	// Size of the database
	Size *Size `json:"size,omitempty"`

	// todo: add default
	// Maintenance defines automatic maintenance of the database
	Maintenance []string `json:"maintenance,omitempty"`

	// todo: add default
	// Backup parametes of the database backup
	Backup *Backup `json:"backup,omitempty"`
	// AccessList defines access restrictions
	AccessList *AccessList `json:"accessList,omitempty"`
}

// AccessList defines the type of restrictions to access the database
type AccessList struct {
	// SourceRanges defines a list of prefixes in CIDR Notation e.g. 1.2.3.0/24 or fdaa::/104
	SourceRanges []string `json:"sourceRanges,omitempty"`
}

// Backup configure parametes of the database backup
type Backup struct {
	// S3BucketURL defines the URL of the S3 bucket for backup
	S3BucketURL string `json:"s3BucketURL,omitempty"`
	// Retention defines how many days a backup will persist
	Retention int32 `json:"retention,omitempty"`
	// Schedule defines how often a backup should be made, in cron format
	Schedule string `json:"schedule,omitempty"`
}

// Size defines the size aspects of the database
type Size struct {
	// CPU is in the format as pod.spec.resource.request.cpu
	CPU string `json:"cpu,omitempty"`
	// Memory is in the format as pod.spec.resource.request.memory
	Memory string `json:"memory,omitempty"`
	// SharedBuffer of the database
	SharedBuffer string `json:"sharedBuffer,omitempty"`

	// StorageSize the amount of Storage this database will get
	// +kubebuilder:default="1Gi"
	// +kubebuilder:validation:Pattern=^[1-9][0-9]*Gi
	StorageSize string `json:"storageSize,omitempty"`
}

// Weekday defines a weekday or everyday
type Weekday int

const (
	Sun Weekday = iota
	Mon
	Tue
	Wed
	Thu
	Fri
	Sat
	All
)

// TimeWindow defines an interval in time
type TimeWindow struct {
	Start metav1.Time `json:"start,omitempty"`
	End   metav1.Time `json:"end,omitempty"`
}

// PostgresStatus defines the observed state of Postgres
type PostgresStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Description string `json:"description,omitempty"`

	Socket Socket `json:"socket,omitempty"`
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
			parsedSrc, err := netaddr.ParseIPPrefix(src)
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

var SvcLoadBalancerLabel = map[string]string{
	"postgres.database.fits.cloud/managed-by": "postgreslet",
}

func (p *Postgres) ToSvcLB(lbIP string, lbPort int32) *corev1.Service {
	lb := &corev1.Service{}
	lb.Spec.Type = "LoadBalancer"

	lb.Annotations = map[string]string{
		"metallb.universe.tf/allow-shared-ip": "spilo",
	}

	lb.Namespace = p.ToPeripheralResourceNamespace()
	lb.Name = p.ToSvcLBName()
	lb.SetLabels(SvcLoadBalancerLabel)

	// svc.Spec.LoadBalancerSourceRanges // todo: Do we need to set this?

	port := corev1.ServicePort{}
	port.Name = "postgresql"
	port.Port = lbPort
	port.Protocol = corev1.ProtocolTCP
	port.TargetPort = intstr.FromInt(5432)
	lb.Spec.Ports = []corev1.ServicePort{port}

	lb.Spec.Selector = map[string]string{
		"application":  "spilo",
		"cluster-name": p.ToPeripheralResourceName(),
		"spilo-role":   "master",
		"team":         p.generateTeamID(),
	}

	if len(lbIP) > 0 {
		// if no ip is set, a new loadbalancer will be created automatically
		lb.Spec.LoadBalancerIP = lbIP
	}

	return lb
}

// ToSvcLBName returns the name of the peripheral resource Service LoadBalancer.
// It's different from all other peripheral resources because the operator
// already generates one service with that name.
func (p *Postgres) ToSvcLBName() string {
	return p.ToPeripheralResourceName() + "-external"
}

func (p *Postgres) ToSvcLBNamespacedName() *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: p.ToPeripheralResourceNamespace(),
		Name:      p.ToSvcLBName(),
	}
}

func (p *Postgres) ToPeripheralResourceName() string {

	return p.generateTeamID() + "-" + p.generateDatabaseName()
}

// ToUserPasswordSecret returns the secret containing user password pairs
func (p *Postgres) ToUserPasswordsSecret(src *corev1.SecretList, scheme *runtime.Scheme) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	secret.Namespace = p.Namespace
	// todo: Consider `p.Name + "-passwords", so the`
	secret.Name = p.ToUserPasswordsSecretName()
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{}

	// Fill in the contents of the new secret
	for _, v := range src.Items {
		secret.Data[string(v.Data["username"])] = v.Data["password"]
	}

	// Set the owner of the secret
	if err := controllerutil.SetControllerReference(p, secret, scheme); err != nil {
		return nil, err
	}

	return secret, nil
}

// ToUserPasswordsSecretName returns the name of the secret containing user password pairs
func (p *Postgres) ToUserPasswordsSecretName() string {
	return p.Name + "-passwords"
}

// ToBackupSecretName returns the name of the secret containing backup credentials
func (p *Postgres) ToBackupSecretName() string {
	return p.Name + "-backup"
}

// ToUserPasswordsSecretListOption returns the argument for listing secrets
func (p *Postgres) ToUserPasswordsSecretListOption() []client.ListOption {
	return []client.ListOption{
		client.InNamespace(p.ToPeripheralResourceNamespace()),
		client.MatchingLabels(
			map[string]string{
				"application":  "spilo",
				"cluster-name": p.ToPeripheralResourceName(),
				"team":         p.generateTeamID(),
			},
		),
	}
}

var alphaNumericRegExp *regexp.Regexp = regexp.MustCompile("[^a-zA-Z0-9]+")

func (p *Postgres) generateTeamID() string {
	// We only want letters and numbers
	generatedTeamID := alphaNumericRegExp.ReplaceAllString(p.Spec.ProjectID, "")

	// Limit size
	maxLen := 16
	if len(generatedTeamID) > maxLen {
		generatedTeamID = generatedTeamID[:maxLen]
	}

	return generatedTeamID
}

func (p *Postgres) generateDatabaseName() string {
	// We only want letters and numbers
	generatedDatabaseName := alphaNumericRegExp.ReplaceAllString(string(p.UID), "")

	// Limit size
	maxLen := 20
	if len(generatedDatabaseName) > maxLen {
		generatedDatabaseName = generatedDatabaseName[:maxLen]
	}

	return generatedDatabaseName
}

func (p *Postgres) ToPeripheralResourceNamespace() string {
	return p.Spec.ProjectID
}

// Name of the label referencing the owning Postgres resource in the control cluster
const LabelName string = "postgres.database.fits.cloud/uuid"

var ZalandoPostgresqlTypeMeta = metav1.TypeMeta{
	APIVersion: "acid.zalan.do/v1",
	Kind:       "postgresql",
}

func (p *Postgres) ToUnstructuredZalandoPostgresql(z *zalando.Postgresql) (*unstructured.Unstructured, error) {
	if z == nil {
		z = &zalando.Postgresql{}
	}
	z.TypeMeta = ZalandoPostgresqlTypeMeta
	z.Namespace = p.ToPeripheralResourceNamespace()
	z.Name = p.ToPeripheralResourceName()
	z.Labels = p.ToZalandoPostgresqlMatchingLabels()

	z.Spec.NumberOfInstances = p.Spec.NumberOfInstances
	z.Spec.PostgresqlParam.PgVersion = p.Spec.Version
	z.Spec.Resources.ResourceRequests.CPU = p.Spec.Size.CPU
	z.Spec.Resources.ResourceRequests.Memory = p.Spec.Size.Memory
	z.Spec.Resources.ResourceLimits.CPU = p.Spec.Size.CPU
	z.Spec.Resources.ResourceLimits.Memory = p.Spec.Size.Memory
	z.Spec.TeamID = p.generateTeamID()
	z.Spec.Volume.Size = p.Spec.Size.StorageSize

	if p.HasSourceRanges() {
		z.Spec.AllowedSourceRanges = p.Spec.AccessList.SourceRanges
	}

	jsonZ, err := runtime.DefaultUnstructuredConverter.ToUnstructured(z)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to unstructured zalando postgresql: %w", err)
	}
	jsonSpec, _ := jsonZ["spec"].(map[string]interface{})
	jsonSpec["maintenanceWindows"] = p.Spec.Maintenance

	// Delete unused fields
	delete(jsonSpec, "clone")
	delete(jsonSpec, "patroni")
	delete(jsonSpec, "podAnnotations")
	delete(jsonSpec, "serviceAnnotations")
	delete(jsonSpec, "standby")
	delete(jsonSpec, "tls")
	delete(jsonSpec, "users")

	jsonP, _ := jsonSpec["postgresql"].(map[string]interface{})
	delete(jsonP, "parameters")

	return &unstructured.Unstructured{
		Object: jsonZ,
	}, nil
}

func (p *Postgres) ToZalandoPostgresqlMatchingLabels() client.MatchingLabels {
	return client.MatchingLabels{LabelName: string(p.UID)}
}

const PostgresFinalizerName = "postgres.finalizers.database.fits.cloud"

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

func init() {
	SchemeBuilder.Register(&Postgres{}, &PostgresList{})
}
