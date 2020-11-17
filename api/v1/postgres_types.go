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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`

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
	ProjectID string `json:"project_id,omitempty"`
	// Tenant metal tenant
	Tenant string `json:"tenant,omitempty"`
	// PartitionID the partition where the database is created
	PartitionID string `json:"partition_id,omitempty"`
	// NumberOfInstances number of replicas
	NumberOfInstances int32 `json:"number_of_instances,omitempty"`
	// Version is the postgres version
	Version string `json:"version,omitempty"`
	// Size of the database
	Size Size `json:"size,omitempty"`
	// Maintenance defines automatic maintenance of the database
	Maintenance Maintenance `json:"maintenance,omitempty"`
	// Backup parametes of the database backup
	Backup Backup `json:"backup,omitempty"`
	// AccessList defines access restrictions
	AccessList AccessList `json:"access_list,omitempty"`
}

// AccessList defines the type of restrictions to access the database
type AccessList struct {
	// SourceRanges defines a list of prefixes in CIDR Notation e.g. 1.2.3.0/24
	// FIXME implement validation if source is a parsable CIDR
	SourceRanges []string `json:"source_ranges,omitempty"`
}

// Backup configure parametes of the database backup
type Backup struct {
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
	SharedBuffer string `json:"shared_buffer,omitempty"`
	// StorageSize the amount of Storage this database will get
	StorageSize string `json:"storage_size,omitempty"`
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
	TimeWindow TimeWindow `json:"time_window,omitempty"`
}

// PostgresStatus defines the observed state of Postgres
type PostgresStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
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

func init() {
	SchemeBuilder.Register(&Postgres{}, &PostgresList{})
}
