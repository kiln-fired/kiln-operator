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
	"strings"
	"time"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("Seed controller", func() {

	const Namespace = "test-namespace"
	const SeedName = "test"
	const SecretName = "seed"
	const Mnemonic = "above pioneer library glimpse exhibit analyst monitor holiday boil art ketchup mail hunt since now pattern vacant arch museum tourist brisk come pilot devote"
	const Passphrase = "test"

	ctx := context.Background()
	seedNamespaceName := types.NamespacedName{Namespace: Namespace, Name: SeedName}
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
		By("cleaning up Seed")
		seed := &bitcoinv1alpha1.Seed{}
		err := k8sClient.Get(ctx, seedNamespaceName, seed)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, seed)
		Expect(err).To(Not(HaveOccurred()))

		By("cleaning up Secret")
		secret := &v1.Secret{}
		err = k8sClient.Get(ctx, secretNamespacedName, secret)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, secret)
		Expect(err).To(Not(HaveOccurred()))
	})

	It("reconciling a Seed instance with no mnemonic", func() {
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SeedName,
				Namespace: Namespace,
			},
			Spec: bitcoinv1alpha1.SeedSpec{
				SecretName: SecretName,
				Network:    "simnet",
			},
		}

		By("creating the custom resource for the kind Seed")
		err := k8sClient.Create(ctx, seed)
		Expect(err).To(Not(HaveOccurred()))

		By("checking if the custom resource was successfully created")
		Eventually(func() error {
			foundSeed := &bitcoinv1alpha1.Seed{}
			return k8sClient.Get(ctx, seedNamespaceName, foundSeed)
		}, time.Minute, time.Second).Should(Succeed())

		By("reconciling the custom resource created")
		seedReconciler := SeedReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		_, err = seedReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: seedNamespaceName,
		})
		Expect(err).To(Not(HaveOccurred()))

		By("checking if a secret was successfully created in the reconciliation")
		foundSecret := &v1.Secret{}
		Eventually(func() error {
			return k8sClient.Get(ctx, secretNamespacedName, foundSecret)
		}, time.Minute, time.Second).Should(Succeed())

		By("checking if a mnemonic was generated")
		Eventually(func() error {
			Expect(foundSecret.Data).To(Not(BeEmpty()))
			Expect(foundSecret.Data["mnemonic"]).To(Not(BeEmpty()))
			Expect(strings.Fields(string(foundSecret.Data["mnemonic"]))).To(HaveLen(24))
			Expect(foundSecret.Data["passphrase"]).To(Not(BeEmpty()))
			Expect(foundSecret.Data["passphrase"]).To(HaveLen(32))
			Expect(foundSecret.Data["rootkey"]).To(Not(BeEmpty()))
			return nil
		}, time.Minute, time.Second).Should(Succeed())

	})

	DescribeTable("reconciling a Seed instance",
		func(network string, hdkey string) {

			seed := &bitcoinv1alpha1.Seed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SeedName,
					Namespace: Namespace,
				},
				Spec: bitcoinv1alpha1.SeedSpec{
					SecretName: SecretName,
					Mnemonic:   Mnemonic,
					Passphrase: Passphrase,
					Network:    network,
				},
			}

			By("creating the custom resource for the kind Seed")
			err := k8sClient.Create(ctx, seed)
			Expect(err).To(Not(HaveOccurred()))

			By("checking if the custom resource was successfully created")
			Eventually(func() error {
				foundSeed := &bitcoinv1alpha1.Seed{}
				return k8sClient.Get(ctx, seedNamespaceName, foundSeed)
			}, time.Minute, time.Second).Should(Succeed())

			By("reconciling the custom resource created")
			seedReconciler := SeedReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = seedReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: seedNamespaceName,
			})
			Expect(err).To(Not(HaveOccurred()))

			By("checking if a secret was successfully created in the reconciliation")
			foundSecret := &v1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, secretNamespacedName, foundSecret)
			}, time.Minute, time.Second).Should(Succeed())

			By("checking if the expected hdkey was derived from the seed")
			Eventually(func() error {
				Expect(foundSecret.Data).To(Not(BeEmpty()))
				Expect(foundSecret.Data["mnemonic"]).To(Not(BeEmpty()))
				Expect(foundSecret.Data["mnemonic"]).To(Equal([]byte(Mnemonic)))
				Expect(foundSecret.Data["passphrase"]).To(Not(BeEmpty()))
				Expect(foundSecret.Data["passphrase"]).To(Equal([]byte(Passphrase)))
				Expect(foundSecret.Data["rootkey"]).To(Not(BeEmpty()))
				Expect(foundSecret.Data["rootkey"]).To(Equal([]byte(hdkey)))
				return nil
			}, time.Minute, time.Second).Should(Succeed())
		},
		Entry(
			"when configuration specifies simnet",
			"simnet",
			"sprv8Erh3X3hFeKunHkJdgLsPuartHeq6F7hf7AbztZnBdVxpxt57x4vLpMB5JYhbryt5Ydn28XYEsMbhW4S1gUJpatAyZqCaco9fsvBfheXzE9",
		),
		Entry(
			"when configuration specifies mainnet",
			"mainnet",
			"xprv9s21ZrQH143K2mhtoUGzSM4Nk8P4oM5CEfmhus3D5fPN6TxDPEtjT8dsBLLdbQFV7kDomWWLYB8M7w8FcAYNomJBKGKKWAtb2WEQcXrtiyY",
		),
	)
})
