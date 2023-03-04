//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright 2023.

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

package v1alpha1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BitcoinConnection) DeepCopyInto(out *BitcoinConnection) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BitcoinConnection.
func (in *BitcoinConnection) DeepCopy() *BitcoinConnection {
	if in == nil {
		return nil
	}
	out := new(BitcoinConnection)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BitcoinNode) DeepCopyInto(out *BitcoinNode) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BitcoinNode.
func (in *BitcoinNode) DeepCopy() *BitcoinNode {
	if in == nil {
		return nil
	}
	out := new(BitcoinNode)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *BitcoinNode) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BitcoinNodeList) DeepCopyInto(out *BitcoinNodeList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]BitcoinNode, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BitcoinNodeList.
func (in *BitcoinNodeList) DeepCopy() *BitcoinNodeList {
	if in == nil {
		return nil
	}
	out := new(BitcoinNodeList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *BitcoinNodeList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BitcoinNodeSpec) DeepCopyInto(out *BitcoinNodeSpec) {
	*out = *in
	out.RPCServer = in.RPCServer
	out.Mining = in.Mining
	in.Resources.DeepCopyInto(&out.Resources)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BitcoinNodeSpec.
func (in *BitcoinNodeSpec) DeepCopy() *BitcoinNodeSpec {
	if in == nil {
		return nil
	}
	out := new(BitcoinNodeSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BitcoinNodeStatus) DeepCopyInto(out *BitcoinNodeStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BitcoinNodeStatus.
func (in *BitcoinNodeStatus) DeepCopy() *BitcoinNodeStatus {
	if in == nil {
		return nil
	}
	out := new(BitcoinNodeStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ChainKey) DeepCopyInto(out *ChainKey) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ChainKey.
func (in *ChainKey) DeepCopy() *ChainKey {
	if in == nil {
		return nil
	}
	out := new(ChainKey)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ChainKey) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ChainKeyList) DeepCopyInto(out *ChainKeyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ChainKey, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ChainKeyList.
func (in *ChainKeyList) DeepCopy() *ChainKeyList {
	if in == nil {
		return nil
	}
	out := new(ChainKeyList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ChainKeyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ChainKeySpec) DeepCopyInto(out *ChainKeySpec) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ChainKeySpec.
func (in *ChainKeySpec) DeepCopy() *ChainKeySpec {
	if in == nil {
		return nil
	}
	out := new(ChainKeySpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ChainKeyStatus) DeepCopyInto(out *ChainKeyStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ChainKeyStatus.
func (in *ChainKeyStatus) DeepCopy() *ChainKeyStatus {
	if in == nil {
		return nil
	}
	out := new(ChainKeyStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LightningNode) DeepCopyInto(out *LightningNode) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LightningNode.
func (in *LightningNode) DeepCopy() *LightningNode {
	if in == nil {
		return nil
	}
	out := new(LightningNode)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *LightningNode) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LightningNodeList) DeepCopyInto(out *LightningNodeList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]LightningNode, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LightningNodeList.
func (in *LightningNodeList) DeepCopy() *LightningNodeList {
	if in == nil {
		return nil
	}
	out := new(LightningNodeList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *LightningNodeList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LightningNodeSpec) DeepCopyInto(out *LightningNodeSpec) {
	*out = *in
	out.BitcoinConnection = in.BitcoinConnection
	out.Wallet = in.Wallet
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LightningNodeSpec.
func (in *LightningNodeSpec) DeepCopy() *LightningNodeSpec {
	if in == nil {
		return nil
	}
	out := new(LightningNodeSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LightningNodeStatus) DeepCopyInto(out *LightningNodeStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LightningNodeStatus.
func (in *LightningNodeStatus) DeepCopy() *LightningNodeStatus {
	if in == nil {
		return nil
	}
	out := new(LightningNodeStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Mining) DeepCopyInto(out *Mining) {
	*out = *in
	out.RewardAddress = in.RewardAddress
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Mining.
func (in *Mining) DeepCopy() *Mining {
	if in == nil {
		return nil
	}
	out := new(Mining)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RPCServer) DeepCopyInto(out *RPCServer) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RPCServer.
func (in *RPCServer) DeepCopy() *RPCServer {
	if in == nil {
		return nil
	}
	out := new(RPCServer)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RewardAddress) DeepCopyInto(out *RewardAddress) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RewardAddress.
func (in *RewardAddress) DeepCopy() *RewardAddress {
	if in == nil {
		return nil
	}
	out := new(RewardAddress)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SeedImport) DeepCopyInto(out *SeedImport) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SeedImport.
func (in *SeedImport) DeepCopy() *SeedImport {
	if in == nil {
		return nil
	}
	out := new(SeedImport)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Wallet) DeepCopyInto(out *Wallet) {
	*out = *in
	out.Password = in.Password
	out.Seed = in.Seed
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Wallet.
func (in *Wallet) DeepCopy() *Wallet {
	if in == nil {
		return nil
	}
	out := new(Wallet)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *WalletPassword) DeepCopyInto(out *WalletPassword) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new WalletPassword.
func (in *WalletPassword) DeepCopy() *WalletPassword {
	if in == nil {
		return nil
	}
	out := new(WalletPassword)
	in.DeepCopyInto(out)
	return out
}
