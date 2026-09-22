package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightningnetwork/lnd/aezeed"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

const retainedSeedAnnotation = "bitcoin.kiln-fired.github.io/retained-seed"

type SeedReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=seeds,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=seeds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch

func (r *SeedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	seed := &bitcoinv1alpha1.Seed{}
	if err := r.Get(ctx, req.NamespacedName, seed); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	seed.Status.SecretName = seed.Spec.SecretName

	foundSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: seed.Spec.SecretName, Namespace: seed.Namespace}, foundSecret)
	if err == nil {
		if ok, migrated := retainedSeedSecretMatches(foundSecret, seed); !ok {
			return r.fail(ctx, seed, "SecretCollision", "target Secret already exists and is not retained seed material for this Seed")
		} else if migrated {
			if foundSecret.Annotations == nil {
				foundSecret.Annotations = map[string]string{}
			}
			foundSecret.Annotations[retainedSeedAnnotation] = seed.Name
			foundSecret.OwnerReferences = removeControllerOwner(foundSecret.OwnerReferences, seed.UID)
			if err := r.Update(ctx, foundSecret); err != nil {
				return ctrl.Result{}, err
			}
		}
		if !seedSecretHasRequiredKeys(foundSecret) {
			return r.fail(ctx, seed, "SecretInvalid", "retained seed Secret is missing required keys")
		}
		return r.ready(ctx, seed)
	}
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	mnemonic, passphrase, reason, err := r.resolveSeedInput(ctx, seed)
	if err != nil {
		return r.fail(ctx, seed, reason, err.Error())
	}

	networkParams, err := seedNetworkParams(seed.Spec.Network)
	if err != nil {
		return r.fail(ctx, seed, "UnsupportedNetwork", err.Error())
	}
	cipherSeed, err := mnemonic.ToCipherSeed([]byte(passphrase))
	if err != nil {
		return r.fail(ctx, seed, "InvalidPassphrase", "seed passphrase does not unlock the mnemonic")
	}
	hdkey, err := hdkeychain.NewMaster(cipherSeed.Entropy[:], networkParams)
	if err != nil {
		return r.fail(ctx, seed, "DerivationFailed", "unable to derive root key from seed material")
	}

	secret := r.secretForSeed(seed, mnemonic, passphrase, hdkey)
	log.Info("Creating retained Seed Secret", "Secret.Namespace", secret.Namespace, "Secret.Name", secret.Name)
	if err := r.Create(ctx, secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return r.fail(ctx, seed, "SecretCollision", "target Secret already exists")
		}
		return ctrl.Result{}, err
	}
	return r.ready(ctx, seed)
}

func (r *SeedReconciler) resolveSeedInput(ctx context.Context, seed *bitcoinv1alpha1.Seed) (aezeed.Mnemonic, string, string, error) {
	if seed.Spec.Import == nil {
		cipherSeed, err := aezeed.New(0, nil, time.Now())
		if err != nil {
			return aezeed.Mnemonic{}, "", "GenerationFailed", err
		}
		passphrase := randSeq(32)
		mnemonic, err := cipherSeed.ToMnemonic([]byte(passphrase))
		if err != nil {
			return aezeed.Mnemonic{}, "", "GenerationFailed", err
		}
		return mnemonic, passphrase, "", nil
	}

	ref := seed.Spec.Import
	input := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.SecretName, Namespace: seed.Namespace}, input); err != nil {
		if errors.IsNotFound(err) {
			return aezeed.Mnemonic{}, "", "ImportSecretNotFound", fmt.Errorf("import Secret %q was not found", ref.SecretName)
		}
		return aezeed.Mnemonic{}, "", "ImportSecretReadFailed", err
	}

	mnemonicKey := ref.MnemonicKey
	if mnemonicKey == "" {
		mnemonicKey = "mnemonic"
	}
	passphraseKey := ref.PassphraseKey
	if passphraseKey == "" {
		passphraseKey = "passphrase"
	}
	mnemonicBytes, ok := input.Data[mnemonicKey]
	if !ok || len(mnemonicBytes) == 0 {
		return aezeed.Mnemonic{}, "", "ImportSecretInvalid", fmt.Errorf("import Secret is missing key %q", mnemonicKey)
	}
	passphraseBytes, ok := input.Data[passphraseKey]
	if !ok {
		return aezeed.Mnemonic{}, "", "ImportSecretInvalid", fmt.Errorf("import Secret is missing key %q", passphraseKey)
	}
	mnemonic, err := initializeMnemonic(string(mnemonicBytes))
	if err != nil {
		return aezeed.Mnemonic{}, "", "InvalidMnemonic", err
	}
	return mnemonic, string(passphraseBytes), "", nil
}

func seedNetworkParams(network string) (*chaincfg.Params, error) {
	if network == "" || network == "simnet" {
		return &chaincfg.SimNetParams, nil
	}
	if network == "mainnet" {
		return &chaincfg.MainNetParams, nil
	}
	return nil, fmt.Errorf("unsupported seed network %q", network)
}

func (r *SeedReconciler) ready(ctx context.Context, seed *bitcoinv1alpha1.Seed) (ctrl.Result, error) {
	meta.SetStatusCondition(&seed.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "SecretReady",
		Message:            "retained seed Secret is ready",
		ObservedGeneration: seed.Generation,
	})
	meta.SetStatusCondition(&seed.Status.Conditions, metav1.Condition{
		Type:               "SecretReady",
		Status:             metav1.ConditionTrue,
		Reason:             "RetainedSecretReady",
		Message:            "seed material is stored in the retained Secret",
		ObservedGeneration: seed.Generation,
	})
	return ctrl.Result{}, r.Status().Update(ctx, seed)
}

func (r *SeedReconciler) fail(ctx context.Context, seed *bitcoinv1alpha1.Seed, reason, message string) (ctrl.Result, error) {
	meta.SetStatusCondition(&seed.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: seed.Generation,
	})
	meta.SetStatusCondition(&seed.Status.Conditions, metav1.Condition{
		Type:               "SecretReady",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: seed.Generation,
	})
	return ctrl.Result{}, r.Status().Update(ctx, seed)
}

func (r *SeedReconciler) secretForSeed(s *bitcoinv1alpha1.Seed, mnemonic aezeed.Mnemonic, passphrase string, hdkey *hdkeychain.ExtendedKey) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    labelsForSeed(s.Name),
			Name:      s.Spec.SecretName,
			Namespace: s.Namespace,
			Annotations: map[string]string{
				retainedSeedAnnotation: s.Name,
			},
		},
		StringData: map[string]string{
			"mnemonic":   strings.Join(mnemonic[:], " "),
			"passphrase": passphrase,
			"rootkey":    hdkey.String(),
		},
	}
}

func retainedSeedSecretMatches(secret *corev1.Secret, seed *bitcoinv1alpha1.Seed) (bool, bool) {
	if secret.Annotations[retainedSeedAnnotation] == seed.Name &&
		secret.Labels["app"] == "seed" &&
		secret.Labels["seed_cr"] == seed.Name {
		return true, false
	}
	ref := metav1.GetControllerOf(secret)
	if ref != nil && ref.UID == seed.UID && ref.Kind == "Seed" {
		return true, true
	}
	return false, false
}

func removeControllerOwner(refs []metav1.OwnerReference, uid types.UID) []metav1.OwnerReference {
	out := refs[:0]
	for _, ref := range refs {
		if ref.UID == uid && ref.Controller != nil && *ref.Controller {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func seedSecretHasRequiredKeys(secret *corev1.Secret) bool {
	return len(secret.Data["mnemonic"]) > 0 && secret.Data["passphrase"] != nil && len(secret.Data["rootkey"]) > 0
}

func labelsForSeed(name string) map[string]string {
	return map[string]string{"app": "seed", "seed_cr": name}
}

func (r *SeedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.Seed{}).
		Complete(r)
}
