/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// LightningChannelSpec defines a desired LND channel.
type LightningChannelSpec struct {
	// Name of the LightningPeer that identifies the remote node.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="peerRef is immutable"
	PeerRef string `json:"peerRef"`

	// Local funding amount committed to the channel.
	// +kubebuilder:validation:Minimum=20000
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="capacitySats is immutable"
	CapacitySats int64 `json:"capacitySats"`

	// Private controls whether the channel is announced to the public graph.
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="private is immutable"
	Private bool `json:"private,omitempty"`

	// MinConfs is the minimum confirmation count required for wallet inputs.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="minConfs is immutable"
	MinConfs int32 `json:"minConfs,omitempty"`

	// SpendUnconfirmed allows unconfirmed wallet outputs to fund the channel.
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spendUnconfirmed is immutable"
	SpendUnconfirmed bool `json:"spendUnconfirmed,omitempty"`

	// Safety policy for network-sensitive behavior.
	Safety NetworkSafetyPolicy `json:"safety,omitempty"`
}

// LightningChannelStatus describes the observed LND channel.
type LightningChannelStatus struct {
	// Phase is a concise summary of channel lifecycle state.
	// +optional
	Phase string `json:"phase,omitempty"`

	// ChannelPoint is the funding outpoint once assigned by LND.
	// +optional
	ChannelPoint string `json:"channelPoint,omitempty"`

	// RemotePubkey is the resolved LightningPeer identity.
	// +optional
	RemotePubkey string `json:"remotePubkey,omitempty"`

	// Active reports whether LND currently considers the channel active.
	Active bool `json:"active,omitempty"`

	// CapacitySats is the observed channel capacity.
	CapacitySats int64 `json:"capacitySats,omitempty"`

	// LocalBalanceSats is this node's current channel balance.
	LocalBalanceSats int64 `json:"localBalanceSats,omitempty"`

	// RemoteBalanceSats is the remote node's current channel balance.
	RemoteBalanceSats int64 `json:"remoteBalanceSats,omitempty"`

	// Private reports the observed channel visibility.
	Private bool `json:"private,omitempty"`

	// Conditions summarize node, peer, funding, and readiness state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Peer",type=string,JSONPath=".spec.peerRef"
// +kubebuilder:printcolumn:name="Capacity",type=integer,JSONPath=".spec.capacitySats"
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=".status.active"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"

// LightningChannel is the Schema for durable LND channel state.
type LightningChannel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LightningChannelSpec   `json:"spec,omitempty"`
	Status LightningChannelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LightningChannelList contains a list of LightningChannel.
type LightningChannelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LightningChannel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LightningChannel{}, &LightningChannelList{})
}
