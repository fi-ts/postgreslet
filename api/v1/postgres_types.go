/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
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
	// +kubebuilder:validation:Enum="12";
	// +kubebuilder:default="12"
	Version string `json:"version,omitempty"`

	// Size of the database
	Size *Size `json:"size,omitempty"`

	// todo: add default
	// Maintenance defines automatic maintenance of the database
	Maintenance *Maintenance `json:"maintenance,omitempty"`

	// todo: add default
	// Backup parametes of the database backup
	Backup *Backup `json:"backup,omitempty"`
	// AccessList defines access restrictions
	AccessList *AccessList `json:"accessList,omitempty"`
}

// AccessList defines the type of restrictions to access the database
type AccessList struct {
	// SourceRanges defines a list of prefixes in CIDR Notation e.g. 1.2.3.0/24
	// FIXME implement validation if source is a parsable CIDR
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

// Maintenance configures database maintenance
type Maintenance struct {
	// Weekday defines when the operator is allowed to do maintenance
	Weekday Weekday `json:"weekday,omitempty"`
	// TimeWindow defines when the maintenance should happen
	TimeWindow TimeWindow `json:"timeWindow,omitempty"`
}

// PostgresStatus defines the observed state of Postgres
type PostgresStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Description string `json:"description,omitempty"`
}

// +kubebuilder:object:root=true

// PostgresList contains a list of Postgres
type PostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Postgres `json:"items"`
}

// IsBeingDeleted returns true if the deletion-timestamp is set
func (p *Postgres) IsBeingDeleted() bool {
	return !p.ObjectMeta.DeletionTimestamp.IsZero()
}

func (p *Postgres) ToKey() *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: p.Namespace,
		Name:      p.Name,
	}
}

func (p *Postgres) ToZalandoPostgres() *ZalandoPostgres {
	projectID := p.Spec.ProjectID
	return &ZalandoPostgres{
		TypeMeta: ZalandoPostgresTypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Spec.ProjectID + "--" + string(p.UID), // todo: "." used to work but not anymore. Make sure the rule is well-documented.
			Namespace: projectID,                               // todo: Check if the projectID is too long for zalando operator.
		},
		Spec: ZalandoPostgresSpec{
			MaintenanceWindows: func() []MaintenanceWindow {
				if p.Spec.Maintenance == nil {
					return nil
				}
				isEvery := p.Spec.Maintenance.Weekday == All
				return []MaintenanceWindow{
					{Everyday: isEvery,
						Weekday: func() time.Weekday {
							if isEvery {
								return time.Weekday(0)
							}
							return time.Weekday(p.Spec.Maintenance.Weekday)
						}(),
						StartTime: p.Spec.Maintenance.TimeWindow.Start,
						EndTime:   p.Spec.Maintenance.TimeWindow.End,
					},
				}
			}(),
			NumberOfInstances: p.Spec.NumberOfInstances,
			PostgresqlParam:   PostgresqlParam{PgVersion: p.Spec.Version},
			Resources: func() *Resources {
				if p.Spec.Size.CPU == "" {
					return nil
				}
				return &Resources{
					ResourceRequests: &ResourceDescription{
						CPU: p.Spec.Size.CPU,
					},
					ResourceLimits: &ResourceDescription{}, // todo: Fill it out.
				}
			}(),
			TeamID: projectID,
			Volume: Volume{Size: p.Spec.Size.StorageSize},
		},
	}
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
