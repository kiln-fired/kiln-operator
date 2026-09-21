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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// LightningPeerSpec defines a desired persistent peer connection for an LND node.
type LightningPeerSpec struct {
	// Name of the LightningNode in the same namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="nodeRef is immutable"
	NodeRef string `json:"nodeRef"`

	// Compressed secp256k1 public key of the remote Lightning node.
	// +kubebuilder:validation:Pattern="^[0-9a-fA-F]{66}$"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="pubkey is immutable"
	Pubkey string `json:"pubkey"`

	// Network address used when establishing the peer connection, for example
	// example.com:9735, 203.0.113.10:9735, or an onion address.
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`
}

// LightningPeerStatus describes the observed LND peer connection.
type LightningPeerStatus struct {
	// Phase is a concise summary of reconciliation state.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Connected reports whether LND currently lists the peer as connected.
	Connected bool `json:"connected,omitempty"`

	// ObservedAddress is the address reported by LND for the active connection.
	// +optional
	ObservedAddress string `json:"observedAddress,omitempty"`

	// Inbound reports whether the active connection was initiated by the remote peer.
	// +optional
	Inbound bool `json:"inbound,omitempty"`

	// Conditions summarize node dependency and peer connection state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=".spec.nodeRef"
// +kubebuilder:printcolumn:name="Connected",type=boolean,JSONPath=".status.connected"
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=".status.observedAddress"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"

// LightningPeer is the Schema for durable LND peer connectivity.
type LightningPeer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LightningPeerSpec   `json:"spec,omitempty"`
	Status LightningPeerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LightningPeerList contains a list of LightningPeer.
type LightningPeerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LightningPeer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LightningPeer{}, &LightningPeerList{})
}
