/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/
package v1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	Spec              ZalandoPostgresSpec   `json:"spec"`
	Status            ZalandoPostgresStatus `json:"status,omitempty"`
}

type ZalandoPostgresSpec struct {
	MaintenanceWindows  []MaintenanceWindow `json:"maintenanceWindows,omitempty"`
	NumberOfInstances   int32               `json:"numberOfInstances"`
	PostgresqlParam     PostgresqlParam     `json:"postgresql"`
	Resources           *Resources          `json:"resources,omitempty"`
	TeamID              string              `json:"teamId"`
	Volume              Volume              `json:"volume"`
	EnableLogicalBackup bool                `json:"enableLogicalBackup,omitempty"`
}

type MaintenanceWindow struct {
	Everyday  bool         `json:"everyday,omitempty"`
	Weekday   time.Weekday `json:"weekday,omitempty"`
	StartTime metav1.Time  `json:"startTime,omitempty"`
	EndTime   metav1.Time  `json:"endTime,omitempty"`
}

type PostgresqlParam struct {
	PgVersion string `json:"version"`
}

type Resources struct {
	ResourceRequests *ResourceDescription `json:"requests,omitempty"`
	ResourceLimits   *ResourceDescription `json:"limits,omitempty"`
}

type ResourceDescription struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type Volume struct {
	Size string `json:"size"`
}

type ZalandoPostgresStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Following zalando postgresql, `P`ostgresClusterStatus in JSON.
	PostgresClusterStatus string `json:"PostgresClusterStatus"`
}

// +kubebuilder:object:root=true

// ZalandoPostgresList contains a list of Postgres
type ZalandoPostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZalandoPostgres `json:"items"`
}

func (z *ZalandoPostgres) ToKey() *types.NamespacedName {
	return &types.NamespacedName{
		Namespace: z.Namespace,
		Name:      z.Name,
	}
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
