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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RPCServer struct {
	// Name of the secret that contains TLS certificates for the RPC server
	CertSecret string `json:"certSecret,omitempty"`

	// Name of the secret that contains RPC API credentials
	ApiAuthSecretName string `json:"apiAuthSecretName,omiteempty"`

	// Name of the secret key that contains RPC API username
	ApiUserSecretKey string `json:"apiUserSecretKey,omitempty"`

	// Name of the secret key that contains RPC API password
	ApiPasswordSecretKey string `json:"apiPasswordSecretKey,omitempty"`
}

type RewardAddress struct {
	// Name of the secret that contains the reward address
	SecretName string `json:"secretName,omitempty"`

	// Name of the secret key that contains the reward address
	// +optional
	// +kubebuilder:default:="np2wkhAddress"
	SecretKey string `json:"secretKey,omitempty"`
}

type Mining struct {
	// CPU Mining Enabled
	// +kubebuilder:default:=false
	CpuMiningEnabled bool `json:"cpuMiningEnabled,omitempty"`

	// Address the should receive block rewards
	// +optional
	RewardAddress RewardAddress `json:"rewardAddress,omitempty"`

	// Minimum number of blocks to mine on initial startup
	// +optional
	// +kubebuilder:default:=0
	MinBlocks int64 `json:"minBlocks,omitempty"`

	// Number of seconds to wait between scheduled block generation
	// +optional
	// +kubebuilder:default:=0
	SecondsPerBlock int64 `json:"secondsPerBlock,omitempty"`
}

// BitcoinNodeSpec defines the desired state of BitcoinNode
type BitcoinNodeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Configuration for the RPC Server
	RPCServer RPCServer `json:"rpcServer,omitempty"`

	// Host and port of peer to connect
	// +optional
	Peer string `json:"peer,omitempty"`

	// Mining configuration
	// +optional
	Mining Mining `json:"mining,omitempty"`

	// The compute resource requirements
	// +optional
	// +kubebuilder:default:={limits: {cpu: "100m", memory: "1Gi"}, requests: {cpu: "50m", memory: "200Mi"}}
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// BitcoinNodeStatus defines the observed state of BitcoinNode
type BitcoinNodeStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	LastBlockCount int64 `json:"LastBlockCount"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// BitcoinNode is the Schema for the bitcoinnodes API
type BitcoinNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BitcoinNodeSpec   `json:"spec,omitempty"`
	Status BitcoinNodeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BitcoinNodeList contains a list of BitcoinNode
type BitcoinNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BitcoinNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BitcoinNode{}, &BitcoinNodeList{})
}
