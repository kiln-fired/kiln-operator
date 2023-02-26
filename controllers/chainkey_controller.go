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

package controllers

import (
	"bytes"
	"context"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

// ChainKeyReconciler reconciles a ChainKey object
type ChainKeyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=chainkeys,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=chainkeys/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=chainkeys/finalizers,verbs=update

func (r *ChainKeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	chainKey := &bitcoinv1alpha1.ChainKey{}
	err := r.Get(ctx, req.NamespacedName, chainKey)

	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Bitcoin resource not found.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ChainKey")
		return ctrl.Result{}, err
	}

	//Reconcile Secret
	foundSecret := &v1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: chainKey.Spec.SecretName, Namespace: chainKey.Namespace}, foundSecret)

	if err != nil && errors.IsNotFound(err) {
		secret := r.secretForChainKey(chainKey)
		log.Info("Creating a new Secret", "Secret.Namespace", secret.Namespace, "Secret.Name", secret.Name)
		err = r.Create(ctx, secret)
		if err != nil {
			log.Error(err, "Failed to create new Secret", "Secret.Namespace", secret.Namespace, "Secret.Name", secret.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Secret")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *ChainKeyReconciler) secretForChainKey(c *bitcoinv1alpha1.ChainKey) *v1.Secret {
	ls := labelsForChainKey(c.Name)

	seed := bip39.NewSeed(c.Spec.Mnemonic, c.Spec.Passphrase)
	masterPrivateKey, _ := bip32.NewMasterKey(seed)
	masterPublicKey := masterPrivateKey.PublicKey()

	// BIP-44
	purposeKey, _ := masterPrivateKey.NewChildKey(49 + 0x80000000) // P2WPKH-nested-in-P2SH (BIP-49)
	coinTypeKey, _ := purposeKey.NewChildKey(0 + 0x80000000)       // Bitcoin (SLIP-0044)
	accountKey, _ := coinTypeKey.NewChildKey(0 + 0x80000000)       // first
	changeKey, _ := accountKey.NewChildKey(0)                      // external
	key, _ := changeKey.NewChildKey(0)                             // first

	var networkParams *chaincfg.Params

	switch c.Spec.Network {
	case "simnet":
		networkParams = &chaincfg.SimNetParams
	case "mainnet":
		networkParams = &chaincfg.MainNetParams
	}

	privateKeyBytes, _ := btcec.PrivKeyFromBytes(key.Key)
	btcwif, _ := btcutil.NewWIF(privateKeyBytes, networkParams, true)
	serializedPubKey := btcwif.SerializePubKey()
	witnessProg := btcutil.Hash160(serializedPubKey)
	addressWitnessPubKeyHash, _ := btcutil.NewAddressWitnessPubKeyHash(witnessProg, networkParams)
	serializedScript, _ := txscript.PayToAddrScript(addressWitnessPubKeyHash)
	addressScriptHash, _ := btcutil.NewAddressScriptHash(serializedScript, networkParams)
	segwitNested := addressScriptHash.EncodeAddress()

	secret := v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    ls,
			Name:      c.Spec.SecretName,
			Namespace: c.Namespace,
		},
		StringData: map[string]string{
			"mnemonic":         c.Spec.Mnemonic,
			"passphrase":       c.Spec.Passphrase,
			"seed":             bytes.NewBuffer(seed).String(),
			"masterPrivateKey": masterPrivateKey.String(),
			"masterPublicKey":  masterPublicKey.String(),
			"bip49Address":     segwitNested,
		},
	}

	err := ctrl.SetControllerReference(c, &secret, r.Scheme)
	if err != nil {
		return nil
	}
	return &secret
}

func labelsForChainKey(name string) map[string]string {
	return map[string]string{"app": "chainkey", "chainkey_cr": name}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ChainKeyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.ChainKey{}).
		Owns(&v1.Secret{}).
		Complete(r)
}
