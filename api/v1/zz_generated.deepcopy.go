//go:build !ignore_autogenerated

/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
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
func (in *BackupConfig) DeepCopyInto(out *BackupConfig) {
	*out = *in
	if in.S3EncryptionKey != nil {
		in, out := &in.S3EncryptionKey, &out.S3EncryptionKey
		*out = new(string)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BackupConfig.
func (in *BackupConfig) DeepCopy() *BackupConfig {
	if in == nil {
		return nil
	}
	out := new(BackupConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Postgres) DeepCopyInto(out *Postgres) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
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
func (in *PostgresConnection) DeepCopyInto(out *PostgresConnection) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresConnection.
func (in *PostgresConnection) DeepCopy() *PostgresConnection {
	if in == nil {
		return nil
	}
	out := new(PostgresConnection)
	in.DeepCopyInto(out)
	return out
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
func (in *PostgresRestore) DeepCopyInto(out *PostgresRestore) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PostgresRestore.
func (in *PostgresRestore) DeepCopy() *PostgresRestore {
	if in == nil {
		return nil
	}
	out := new(PostgresRestore)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PostgresSpec) DeepCopyInto(out *PostgresSpec) {
	*out = *in
	if in.Size != nil {
		in, out := &in.Size, &out.Size
		*out = new(Size)
		**out = **in
	}
	if in.Maintenance != nil {
		in, out := &in.Maintenance, &out.Maintenance
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.AccessList != nil {
		in, out := &in.AccessList, &out.AccessList
		*out = new(AccessList)
		(*in).DeepCopyInto(*out)
	}
	if in.PostgresRestore != nil {
		in, out := &in.PostgresRestore, &out.PostgresRestore
		*out = new(PostgresRestore)
		**out = **in
	}
	if in.PostgresConnection != nil {
		in, out := &in.PostgresConnection, &out.PostgresConnection
		*out = new(PostgresConnection)
		**out = **in
	}
	if in.AuditLogs != nil {
		in, out := &in.AuditLogs, &out.AuditLogs
		*out = new(bool)
		**out = **in
	}
	if in.PostgresParams != nil {
		in, out := &in.PostgresParams, &out.PostgresParams
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	if in.DedicatedLoadBalancerIP != nil {
		in, out := &in.DedicatedLoadBalancerIP, &out.DedicatedLoadBalancerIP
		*out = new(string)
		**out = **in
	}
	if in.DedicatedLoadBalancerPort != nil {
		in, out := &in.DedicatedLoadBalancerPort, &out.DedicatedLoadBalancerPort
		*out = new(int32)
		**out = **in
	}
	if in.DisableLoadBalancers != nil {
		in, out := &in.DisableLoadBalancers, &out.DisableLoadBalancers
		*out = new(bool)
		**out = **in
	}
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
	out.Socket = in.Socket
	if in.AdditionalSockets != nil {
		in, out := &in.AdditionalSockets, &out.AdditionalSockets
		*out = make([]Socket, len(*in))
		copy(*out, *in)
	}
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
func (in *Socket) DeepCopyInto(out *Socket) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Socket.
func (in *Socket) DeepCopy() *Socket {
	if in == nil {
		return nil
	}
	out := new(Socket)
	in.DeepCopyInto(out)
	return out
}
