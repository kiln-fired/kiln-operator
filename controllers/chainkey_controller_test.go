package controllers

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("Chainkey controller", func() {

	const ChainKeyName = "test"
	const SecretName = "chain-key"
	const Mnemonic = "voyage blind unit shoulder yellow attitude mule all hire above obvious swap"
	const Passphrase = "123456" // I've got the same combination on my luggage!
	const Network = "mainnet"

	const MasterPrivateKey = "xprv9s21ZrQH143K45MBGTeN7zrQxBgh7v3XNtAMrQvYBfm6xdtaVkjCFNyFHZ262PpMoiaA8JEFGUDPVV6qzB459nGgR1mjuigdTaG2NsKr5BG"
	const MasterPublicKey = "xpub661MyMwAqRbcGZReNVBNV8o9WDXBXNmNk75xeoL9k1J5qSDj3J3SoBHj8rub9x5FDaWqmPEPoBm2zQNTLnkeA2mGbKVfGqXxH36GKtciwFB"
	const BIP49Address = "3GPKjBFRrXnmKLHJtqbiBgXQx9N4UQQ1m3"

	ctx := context.Background()

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ChainKeyName,
			Namespace: ChainKeyName,
		},
	}

	chainKeyNamespaceName := types.NamespacedName{Namespace: ChainKeyName, Name: ChainKeyName}
	secretNamespacedName := types.NamespacedName{Namespace: ChainKeyName, Name: SecretName}

	BeforeEach(func() {
		By("Creating namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))
	})

	AfterEach(func() {
		By("Deleting the test namespace")
		_ = k8sClient.Delete(ctx, namespace)
	})

	It("Should successfully reconcile a custom resource for ChainKey", func() {
		By("Creating the custom resource for the kind ChainKey")
		chainKey := &bitcoinv1alpha1.ChainKey{}
		found := &v1.Secret{}
		err := k8sClient.Get(ctx, chainKeyNamespaceName, chainKey)
		if err != nil && errors.IsNotFound(err) {
			chainKey := &bitcoinv1alpha1.ChainKey{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ChainKeyName,
					Namespace: namespace.Name,
				},
				Spec: bitcoinv1alpha1.ChainKeySpec{
					SecretName: SecretName,
					Mnemonic:   Mnemonic,
					Passphrase: Passphrase,
					Network:    Network,
				},
			}
			err = k8sClient.Create(ctx, chainKey)
			Expect(err).To(Not(HaveOccurred()))
		}

		By("Checking if the custom resource was successfully created")
		Eventually(func() error {
			found := &bitcoinv1alpha1.ChainKey{}
			return k8sClient.Get(ctx, chainKeyNamespaceName, found)
		}, time.Minute, time.Second).Should(Succeed())

		By("Reconciling the custom resource created")
		chainKeyReconciler := ChainKeyReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err = chainKeyReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: chainKeyNamespaceName,
		})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if a secret was successfully created in the reconciliation")
		Eventually(func() error {
			return k8sClient.Get(ctx, secretNamespacedName, found)
		}, time.Minute, time.Second).Should(Succeed())

		By("Checking if the seed and passphrase resulted in the expected key pair")
		Eventually(func() error {
			Expect(found.Data).To(Not(BeEmpty()))
			Expect(found.Data["masterPrivateKey"]).To(Not(BeEmpty()))
			Expect(found.Data["masterPrivateKey"]).To(Equal([]byte(MasterPrivateKey)))
			Expect(found.Data["masterPublicKey"]).To(Not(BeEmpty()))
			Expect(found.Data["masterPublicKey"]).To(Equal([]byte(MasterPublicKey)))
			Expect(found.Data["bip49Address"]).To(Not(BeEmpty()))
			Expect(found.Data["bip49Address"]).To(Equal([]byte(BIP49Address)))
			return nil
		}, time.Minute, time.Second).Should(Succeed())
	})
})
