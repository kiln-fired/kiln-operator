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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChainKeySpec defines the desired state of ChainKey
type ChainKeySpec struct {
	// Name of secret to store master key, chain keypair, and chain address
	SecretName string `json:"secretName"`

	// BIP39 mnemonic phrase (only 24-word format supported)
	// +optional
	Mnemonic string `json:"mnemonic,omitempty"`

	// User supplied passphrase for to generate HD seed
	// +optional
	Passphrase string `json:"passphrase,omitempty"`

	// Bitcoin network, e.g. simnet, testnet, regressionnet, mainnet
	// +kubebuilder:default:="simnet"
	Network string `json:"network,omitempty"`
}

// ChainKeyStatus defines the observed state of ChainKey
type ChainKeyStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ChainKey is the Schema for the chainkeys API
type ChainKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChainKeySpec   `json:"spec,omitempty"`
	Status ChainKeyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ChainKeyList contains a list of ChainKey
type ChainKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChainKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChainKey{}, &ChainKeyList{})
}
