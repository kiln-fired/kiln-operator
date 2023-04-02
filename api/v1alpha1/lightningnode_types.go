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

type LNDContainerImages struct {

	// LND container image
	// +kubebuilder:default:="docker.io/lightninglabs/lndinit:v0.15.5-beta"
	LndImage string `json:"lndImage,omitempty"`

	// lnd-init container image
	// +kubebuilder:default:="docker.io/lightninglabs/lndinit:v0.1.8-beta-lnd-v0.15.5-beta"
	LndInitImage string `json:"lndInitImage,omitemply"`
}

type BitcoinConnection struct {
	// Hostname of the Bitcoin node RPC endpoint
	Host string `json:"host,omitEmpty"`

	// Bitcoin network, e.g. simnet, testnet, regressionnet, mainnet
	// +kubebuilder:default:="simnet"
	Network string `json:"network,omitEmpty"`

	// Name of the secret that contains TLS certificates for the RPC server
	CertSecret string `json:"certSecret,omitempty"`

	// Name of the secret that contains bitcoin node RPC API credentials
	ApiAuthSecretName string `json:"apiAuthSecretName,omiteempty"`

	// Name of the secret key that contains bitcoin node RPC API username
	ApiUserSecretKey string `json:"apiUserSecretKey,omitempty"`

	// Name of the secret key that contains bitcoin node RPC API password
	ApiPasswordSecretKey string `json:"apiPasswordSecretKey,omitempty"`
}

type WalletPassword struct {
	// Name of the secret that contains the Lightning wallet password
	SecretName string `json:"secretName,omitempty"`

	// Name of the secret key that contains the wallet password
	SecretKey string `json:"secretKey,omitempty"`
}

type SeedImport struct {
	// Name of the secret that contains the seed to import
	SecretName string `json:"secretName,omitempty"`

	// Name of the secret key that contains the mnemonic seed
	// +kubebuilder:default:="mnemonic"
	MnemonicKey string `json:"menomicKey,omitempty"`

	// Name of the secret key that contains the seed passphrase
	// +kubebuilder:default:="passphrase"
	PassphraseKey string `json:"passphraseKey,omitempty"`
}

type Wallet struct {
	// Wallet password
	Password WalletPassword `json:"password,omitempty"`

	// Seed to import to the wallet
	Seed SeedImport `json:"seed,omitempty"`
}

// LightningNodeSpec defines the desired state of LightningNode
type LightningNodeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Container image overrides
	ContainerImages LNDContainerImages `json:"image,omitempty"`

	// Configuration for the Bitcoin RPC client
	BitcoinConnection BitcoinConnection `json:"bitcoinConnection,omitempty"`

	// Configuration for the wallet
	Wallet Wallet `json:"wallet,omitempty"`
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
