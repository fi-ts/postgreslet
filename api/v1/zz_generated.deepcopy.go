// +build !ignore_autogenerated

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

// Code generated by controller-gen. DO NOT EDIT.

package v1

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AccessList) DeepCopyInto(out *AccessList) {
	*out = *in
	if in.SourceRanges != nil {
		in, out := &in.SourceRanges, &out.SourceRanges
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AccessList.
func (in *AccessList) DeepCopy() *AccessList {
	if in == nil {
		return nil
	}
	out := new(AccessList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Backup) DeepCopyInto(out *Backup) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Backup.
func (in *Backup) DeepCopy() *Backup {
	if in == nil {
		return nil
	}
	out := new(Backup)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Maintenance) DeepCopyInto(out *Maintenance) {
	*out = *in
	in.TimeWindow.DeepCopyInto(&out.TimeWindow)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Maintenance.
func (in *Maintenance) DeepCopy() *Maintenance {
	if in == nil {
		return nil
	}
	out := new(Maintenance)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Operator) DeepCopyInto(out *Operator) {
	*out = *in
	if in.Version != nil {
		in, out := &in.Version, &out.Version
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Operator.
func (in *Operator) DeepCopy() *Operator {
	if in == nil {
		return nil
	}
	out := new(Operator)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Postgres) DeepCopyInto(out *Postgres) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Postgres.
func (in *Postgres) DeepCopy() *Postgres {
	if in == nil {
		return nil
	}
	out := new(Postgres)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *Postgres) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresList) DeepCopyInto(out *PostgresList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Postgres, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresList.
func (in *PostgresList) DeepCopy() *PostgresList {
	if in == nil {
		return nil
	}
	out := new(PostgresList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *PostgresList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresProfile) DeepCopyInto(out *PostgresProfile) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresProfile.
func (in *PostgresProfile) DeepCopy() *PostgresProfile {
	if in == nil {
		return nil
	}
	out := new(PostgresProfile)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *PostgresProfile) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresProfileList) DeepCopyInto(out *PostgresProfileList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]PostgresProfile, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresProfileList.
func (in *PostgresProfileList) DeepCopy() *PostgresProfileList {
	if in == nil {
		return nil
	}
	out := new(PostgresProfileList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *PostgresProfileList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresProfileSpec) DeepCopyInto(out *PostgresProfileSpec) {
	*out = *in
	if in.NumberOfInstances != nil {
		in, out := &in.NumberOfInstances, &out.NumberOfInstances
		*out = make([]int, len(*in))
		copy(*out, *in)
	}
	if in.Operators != nil {
		in, out := &in.Operators, &out.Operators
		*out = make([]Operator, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Sizes != nil {
		in, out := &in.Sizes, &out.Sizes
		*out = make([]Size, len(*in))
		copy(*out, *in)
	}
	if in.Versions != nil {
		in, out := &in.Versions, &out.Versions
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	out.DefaultBackup = in.DefaultBackup
	in.DefaultMaintenance.DeepCopyInto(&out.DefaultMaintenance)
	in.DefaultOperator.DeepCopyInto(&out.DefaultOperator)
	out.DefaultSize = in.DefaultSize
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresProfileSpec.
func (in *PostgresProfileSpec) DeepCopy() *PostgresProfileSpec {
	if in == nil {
		return nil
	}
	out := new(PostgresProfileSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresProfileStatus) DeepCopyInto(out *PostgresProfileStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresProfileStatus.
func (in *PostgresProfileStatus) DeepCopy() *PostgresProfileStatus {
	if in == nil {
		return nil
	}
	out := new(PostgresProfileStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresSpec) DeepCopyInto(out *PostgresSpec) {
	*out = *in
	out.Size = in.Size
	in.Maintenance.DeepCopyInto(&out.Maintenance)
	out.Backup = in.Backup
	in.AccessList.DeepCopyInto(&out.AccessList)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresSpec.
func (in *PostgresSpec) DeepCopy() *PostgresSpec {
	if in == nil {
		return nil
	}
	out := new(PostgresSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresStatus) DeepCopyInto(out *PostgresStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresStatus.
func (in *PostgresStatus) DeepCopy() *PostgresStatus {
	if in == nil {
		return nil
	}
	out := new(PostgresStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Size) DeepCopyInto(out *Size) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Size.
func (in *Size) DeepCopy() *Size {
	if in == nil {
		return nil
	}
	out := new(Size)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *TimeWindow) DeepCopyInto(out *TimeWindow) {
	*out = *in
	in.Start.DeepCopyInto(&out.Start)
	in.End.DeepCopyInto(&out.End)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new TimeWindow.
func (in *TimeWindow) DeepCopy() *TimeWindow {
	if in == nil {
		return nil
	}
	out := new(TimeWindow)
	in.DeepCopyInto(out)
	return out
}
