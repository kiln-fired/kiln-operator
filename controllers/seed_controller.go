package controllers

import (
	"context"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightningnetwork/lnd/aezeed"
	"strings"
	"time"

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

// SeedReconciler reconciles a Seed object
type SeedReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=seeds,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=seeds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=seeds/finalizers,verbs=update

func (r *SeedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	seed := &bitcoinv1alpha1.Seed{}
	err := r.Get(ctx, req.NamespacedName, seed)

	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Seed resource not found.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Seed")
		return ctrl.Result{}, err
	}

	mnemonicStr := seed.Spec.Mnemonic
	passphraseStr := seed.Spec.Passphrase
	mnemonic := aezeed.Mnemonic{}

	if mnemonicStr == "" {
		cipherSeed, err := aezeed.New(0, nil, time.Now())
		if err != nil {
			log.Error(err, "Failed to generate a random seed")
			return ctrl.Result{}, err
		}

		if passphraseStr == "" {
			passphraseStr = randSeq(32)
		}

		mnemonic, err = cipherSeed.ToMnemonic([]byte(passphraseStr))
		if err != nil {
			log.Error(err, "Failed to generate a random seed")
			return ctrl.Result{}, err
		}

	} else {
		mnemonic, err = initializeMnemonic(mnemonicStr)

		if err != nil {
			log.Error(err, "Failed to initialize mnemonic")
			return ctrl.Result{}, err
		}
	}

	pass := []byte(passphraseStr)
	cipherSeed, err := mnemonic.ToCipherSeed(pass)

	if err != nil {
		log.Error(err, "Failed to generate cipher seed")
		return ctrl.Result{}, err
	}

	var networkParams *chaincfg.Params

	switch seed.Spec.Network {
	case "simnet":
		networkParams = &chaincfg.SimNetParams
	case "mainnet":
		networkParams = &chaincfg.MainNetParams
	}

	hdkey, err := hdkeychain.NewMaster(cipherSeed.Entropy[:], networkParams)

	if err != nil {
		log.Error(err, "Failed to get Seed")
		return ctrl.Result{}, err
	}

	//Reconcile Secret
	foundSecret := &v1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: seed.Spec.SecretName, Namespace: seed.Namespace}, foundSecret)

	if err != nil && errors.IsNotFound(err) {
		secret := r.secretForSeed(seed, mnemonic, passphraseStr, hdkey)
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

func (r *SeedReconciler) secretForSeed(s *bitcoinv1alpha1.Seed, mnemonic aezeed.Mnemonic, passphrase string, hdkey *hdkeychain.ExtendedKey) *v1.Secret {
	ls := labelsForSeed(s.Name)

	secret := v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    ls,
			Name:      s.Spec.SecretName,
			Namespace: s.Namespace,
		},
		StringData: map[string]string{
			"mnemonic":   strings.Join(mnemonic[:], " "),
			"passphrase": passphrase,
			"rootkey":    hdkey.String(),
		},
	}

	err := ctrl.SetControllerReference(s, &secret, r.Scheme)
	if err != nil {
		return nil
	}
	return &secret
}

func labelsForSeed(name string) map[string]string {
	return map[string]string{"app": "seed", "seed_cr": name}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SeedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.Seed{}).
		Complete(r)
}
