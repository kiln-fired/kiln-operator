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

type BitcoinConnection struct {
	Host       string `json:"host,omitEmpty"`
	Network    string `json:"network,omitEmpty"`
	CertSecret string `json:"certSecret,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
}

// LightningNodeSpec defines the desired state of LightningNode
type LightningNodeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	BitcoinConnection BitcoinConnection `json:"bitcoinConnection,omitempty"`
}

// LightningNodeStatus defines the observed state of LightningNode
type LightningNodeStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// LightningNode is the Schema for the lightningnodes API
type LightningNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LightningNodeSpec   `json:"spec,omitempty"`
	Status LightningNodeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// LightningNodeList contains a list of LightningNode
type LightningNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LightningNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LightningNode{}, &LightningNodeList{})
}
