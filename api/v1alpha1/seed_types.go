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

// SeedSpec defines the desired state of Seed
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="seed configuration is immutable"
type SeedSpec struct {
	// Name of the retained Secret that stores generated/imported seed material.
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`

	// Optional Secret reference containing aezeed mnemonic and passphrase input.
	// When omitted, Kiln generates new seed material.
	// +optional
	Import *SeedImport `json:"import,omitempty"`

	// Bitcoin network used to derive the root key.
	// +kubebuilder:default="simnet"
	// +kubebuilder:validation:Enum=simnet;mainnet
	Network string `json:"network,omitempty"`
}

// SeedStatus defines the observed state of Seed.
type SeedStatus struct {
	// SecretName is the retained Secret containing the resulting seed material.
	SecretName string `json:"secretName,omitempty"`

	// Conditions summarize seed generation/import and retained Secret publication.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Seed is the Schema for the seeds API
type Seed struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeedSpec   `json:"spec,omitempty"`
	Status SeedStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SeedList contains a list of Seed
type SeedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Seed `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Seed{}, &SeedList{})
}
