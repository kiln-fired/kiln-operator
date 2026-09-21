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
	// +kubebuilder:default="docker.io/lightninglabs/lnd:v0.21.0-beta"
	LndImage string `json:"lndImage,omitempty"`

	// lnd-init container image
	// +kubebuilder:default="docker.io/lightninglabs/lndinit:v0.1.36-beta-lnd-v0.21.0-beta"
	LndInitImage string `json:"lndInitImage,omitempty"`
}

type ExternalBitcoinConnection struct {
	// Hostname of the externally managed Bitcoin RPC endpoint.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Bitcoin network used by the external backend.
	// +kubebuilder:validation:Enum=simnet;testnet;regtest;signet;mainnet
	Network string `json:"network"`

	// Name of the Secret containing the Bitcoin RPC TLS certificate.
	// +kubebuilder:validation:MinLength=1
	CertSecret string `json:"certSecret"`

	// Name of the Secret containing Bitcoin RPC API credentials.
	// +kubebuilder:validation:MinLength=1
	ApiAuthSecretName string `json:"apiAuthSecretName"`

	// Secret key containing the Bitcoin RPC username.
	// +kubebuilder:validation:MinLength=1
	ApiUserSecretKey string `json:"apiUserSecretKey"`

	// Secret key containing the Bitcoin RPC password.
	// +kubebuilder:validation:MinLength=1
	ApiPasswordSecretKey string `json:"apiPasswordSecretKey"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.nodeRef) && !has(self.external)) || (!has(self.nodeRef) && has(self.external))",message="exactly one of nodeRef or external must be configured"
type BitcoinConnection struct {
	// Name of a BitcoinNode in the same namespace. Kiln derives network, RPC
	// endpoint, TLS Secret, and API credential Secret from the referenced node.
	// +optional
	// +kubebuilder:validation:MinLength=1
	NodeRef string `json:"nodeRef,omitempty"`

	// Explicit connection to an externally managed btcd node.
	// +optional
	External *ExternalBitcoinConnection `json:"external,omitempty"`
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
	// +kubebuilder:default="mnemonic"
	MnemonicKey string `json:"mnemonicKey,omitempty"`

	// Name of the secret key that contains the seed passphrase
	// +kubebuilder:default="passphrase"
	PassphraseKey string `json:"passphraseKey,omitempty"`
}

type Wallet struct {
	// Wallet password
	Password WalletPassword `json:"password,omitempty"`

	// Seed to import to the wallet
	Seed SeedImport `json:"seed,omitempty"`
}

type LightningRuntimeStatus struct {
	// IdentityPubkey is the persistent LND node identity.
	IdentityPubkey string `json:"identityPubkey,omitempty"`
	// Alias is the node alias reported by LND.
	Alias string `json:"alias,omitempty"`
	// Version is the running LND version.
	Version string `json:"version,omitempty"`
	// BlockHeight is LND's current best block height.
	BlockHeight uint32 `json:"blockHeight,omitempty"`
	// SyncedToChain reports whether the wallet is synchronized with Bitcoin.
	SyncedToChain bool `json:"syncedToChain,omitempty"`
	// SyncedToGraph reports whether LND considers its public graph synchronized.
	SyncedToGraph bool `json:"syncedToGraph,omitempty"`
	NumPeers uint32 `json:"numPeers,omitempty"`
	NumPendingChannels uint32 `json:"numPendingChannels,omitempty"`
	NumActiveChannels uint32 `json:"numActiveChannels,omitempty"`
	NumInactiveChannels uint32 `json:"numInactiveChannels,omitempty"`
}

type RPCPublishing struct {
	// Name of the Secret that receives LND client credentials.
	// Defaults to <lightningNode name>-rpc.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// LightningNodeSpec defines the desired state of LightningNode
type LightningNodeSpec struct {
	// Container image overrides
	// +kubebuilder:default={lndImage: "docker.io/lightninglabs/lnd:v0.21.0-beta", lndInitImage: "docker.io/lightninglabs/lndinit:v0.1.36-beta-lnd-v0.21.0-beta"}
	ContainerImages LNDContainerImages `json:"image,omitempty"`

	// Configuration for the Bitcoin RPC client
	BitcoinConnection BitcoinConnection `json:"bitcoinConnection,omitempty"`

	// Configuration for the wallet
	Wallet Wallet `json:"wallet,omitempty"`

	// Safety policy for network-sensitive behavior.
	Safety NetworkSafetyPolicy `json:"safety,omitempty"`

	// RPC client credential publishing configuration
	RPC RPCPublishing `json:"rpc,omitempty"`
}

// LightningNodeStatus defines the observed state of LightningNode
type LightningNodeStatus struct {
	// Phase is a concise summary of the current lifecycle state.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Network is the resolved Bitcoin network used by LND.
	// +optional
	Network string `json:"network,omitempty"`

	// Name of the Secret containing published LND client credentials.
	// +optional
	RPCSecretName string `json:"rpcSecretName,omitempty"`

	// RPCAddress is the in-cluster LND RPC endpoint.
	// +optional
	RPCAddress string `json:"rpcAddress,omitempty"`

	// Runtime contains authenticated observations from LND GetInfo.
	// +optional
	Runtime LightningRuntimeStatus `json:"runtime,omitempty"`

	// Conditions summarize dependency, wallet, credential, storage, and readiness state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
