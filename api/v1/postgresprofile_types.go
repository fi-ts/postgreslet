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

// PostgresProfile is the Schema for the postgresprofiles API
type PostgresProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresProfileSpec   `json:"spec,omitempty"`
	Status PostgresProfileStatus `json:"status,omitempty"`
}

// PostgresProfileSpec defines the desired state of PostgresProfile
type PostgresProfileSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	NumberOfInstances []int      `json:"numberOfInstances,omitempty"`
	Operators         []Operator `json:"operators,omitempty"`
	Sizes             []Size     `json:"sizes,omitempty"`
	Versions          []string   `json:"versions,omitempty"`

	DefaultBackup            Backup      `json:"defaultBackup,omitempty"`
	DefaultMaintenance       Maintenance `json:"defaultMaintenance,omitempty"`
	DefaultNumberOfInstances int         `json:"defaultNumberOfInstances,omitempty"`
	DefaultOperator          Operator    `json:"defaultOperator,omitempty"`
	DefaultSize              Size        `json:"defaultSize,omitempty"`
	DefaultVersion           string      `json:"defaultVersion,omitempty"`
}

// Operator defines which provider of the operator should be used and its version
type Operator struct {
	Provider string   `json:"provider,omitempty"`
	Version  []string `json:"version,omitempty"`
}

// PostgresProfileStatus defines the observed state of PostgresProfile
type PostgresProfileStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true

// PostgresProfileList contains a list of PostgresProfile
type PostgresProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresProfile{}, &PostgresProfileList{})
}
