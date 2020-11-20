package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ZalandoPostgresTypeMeta is the `TypeMeta` of the zalando's `Postgresql` type.
// Only this `TypeMeta` should be used for the resources in this file.
var ZalandoPostgresTypeMeta = metav1.TypeMeta{
	APIVersion: "acid.zalan.do/v1",
	Kind:       "postgresql",
}

// +kubebuilder:object:root=true

type ZalandoPostgres struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ZalandoPostgresSpec `json:"spec"`
}

type ZalandoPostgresSpec struct {
	NumberOfInstances int32           `json:"numberOfInstances"`
	TeamID            string          `json:"teamId"`
	PostgresqlParam   PostgresqlParam `json:"postgresql"`
	Volume            Volume          `json:"volume"`
}

type PostgresqlParam struct {
	PgVersion string `json:"version"`
}
type Volume struct {
	Size string `json:"size"`
}

// +kubebuilder:object:root=true

// ZalandoPostgresList contains a list of Postgres
type ZalandoPostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZalandoPostgres `json:"items"`
}

func (z *ZalandoPostgres) ToUnstructured() (*unstructured.Unstructured, error) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(z)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{
		Object: u,
	}, nil
}

func init() {
	SchemeBuilder.Register(&ZalandoPostgres{}, &ZalandoPostgresList{})
}
