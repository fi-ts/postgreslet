package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true

type ZalandoPostgres struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ZalandoPostgresSpec `json:"spec"`
}

type ZalandoPostgresSpec struct {
	NumberOfInstances int32             `json:"numberOfInstances"`
	TeamID            string            `json:"teamId"`
	Postgresql        ZalandoPostgresql `json:"postgresql"`
	Volume            ZalandoVolume     `json:"volume"`
}

type ZalandoPostgresql struct {
	Version string `json:"version"`
}
type ZalandoVolume struct {
	Size string `json:"size"`
}

// +kubebuilder:object:root=true

// ZalandoPostgresList contains a list of Postgres
type ZalandoPostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZalandoPostgres `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ZalandoPostgres{}, &ZalandoPostgresList{})
}
