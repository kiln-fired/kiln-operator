package controllers

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("ChainKey controller", func() {

	const Namespace = "test-namespace"
	const ChainKeyName = "test"
	const SecretName = "chain-key"
	const Mnemonic = "voyage blind unit shoulder yellow attitude mule all hire above obvious swap"
	const Passphrase = "123456" // I've got the same combination on my luggage!

	const MasterPrivateKey = "xprv9s21ZrQH143K45MBGTeN7zrQxBgh7v3XNtAMrQvYBfm6xdtaVkjCFNyFHZ262PpMoiaA8JEFGUDPVV6qzB459nGgR1mjuigdTaG2NsKr5BG"
	const MasterPublicKey = "xpub661MyMwAqRbcGZReNVBNV8o9WDXBXNmNk75xeoL9k1J5qSDj3J3SoBHj8rub9x5FDaWqmPEPoBm2zQNTLnkeA2mGbKVfGqXxH36GKtciwFB"

	ctx := context.Background()
	chainKeyNamespaceName := types.NamespacedName{Namespace: Namespace, Name: ChainKeyName}
	secretNamespacedName := types.NamespacedName{Namespace: Namespace, Name: SecretName}

	BeforeEach(func() {
		By("creating namespace to perform the tests")
		_ = k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      Namespace,
				Namespace: Namespace,
			},
		})
	})

	AfterEach(func() {
		By("cleaning up ChainKey")
		chainKey := &bitcoinv1alpha1.ChainKey{}
		err := k8sClient.Get(ctx, chainKeyNamespaceName, chainKey)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, chainKey)
		Expect(err).To(Not(HaveOccurred()))

		By("cleaning up Secret")
		secret := &v1.Secret{}
		err = k8sClient.Get(ctx, secretNamespacedName, secret)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, secret)
		Expect(err).To(Not(HaveOccurred()))
	})

	DescribeTable("reconciling a ChainKey instance",
		func(network string, bip49address string) {

			chainKey := &bitcoinv1alpha1.ChainKey{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ChainKeyName,
					Namespace: Namespace,
				},
				Spec: bitcoinv1alpha1.ChainKeySpec{
					SecretName: SecretName,
					Mnemonic:   Mnemonic,
					Passphrase: Passphrase,
					Network:    network,
				},
			}

			By("creating the custom resource for the kind ChainKey")
			err := k8sClient.Create(ctx, chainKey)
			Expect(err).To(Not(HaveOccurred()))

			By("checking if the custom resource was successfully created")
			Eventually(func() error {
				foundChainKey := &bitcoinv1alpha1.ChainKey{}
				return k8sClient.Get(ctx, chainKeyNamespaceName, foundChainKey)
			}, time.Minute, time.Second).Should(Succeed())

			By("reconciling the custom resource created")
			chainKeyReconciler := ChainKeyReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = chainKeyReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: chainKeyNamespaceName,
			})
			Expect(err).To(Not(HaveOccurred()))

			By("checking if a secret was successfully created in the reconciliation")
			foundSecret := &v1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, secretNamespacedName, foundSecret)
			}, time.Minute, time.Second).Should(Succeed())

			By("checking if the seed and passphrase resulted in the expected key pair")
			Eventually(func() error {
				Expect(foundSecret.Data).To(Not(BeEmpty()))
				Expect(foundSecret.Data["masterPrivateKey"]).To(Not(BeEmpty()))
				Expect(foundSecret.Data["masterPrivateKey"]).To(Equal([]byte(MasterPrivateKey)))
				Expect(foundSecret.Data["masterPublicKey"]).To(Not(BeEmpty()))
				Expect(foundSecret.Data["masterPublicKey"]).To(Equal([]byte(MasterPublicKey)))
				Expect(foundSecret.Data["bip49Address"]).To(Not(BeEmpty()))
				Expect(foundSecret.Data["bip49Address"]).To(Equal([]byte(bip49address)))
				return nil
			}, time.Minute, time.Second).Should(Succeed())
		},
		Entry("when configuration specifies simnet", "simnet", "rkGVuzRRdpU9pUjXnLuKQUeFDfmNT47kuW"),
		Entry("when configuration specifies mainnet", "mainnet", "3GPKjBFRrXnmKLHJtqbiBgXQx9N4UQQ1m3"),
	)
})
