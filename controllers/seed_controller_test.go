package controllers

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("Seed controller", func() {
	const Namespace = "test-namespace"
	const SeedName = "test"
	const SecretName = "seed"
	const ImportSecretName = "seed-import"
	const Mnemonic = "above pioneer library glimpse exhibit analyst monitor holiday boil art ketchup mail hunt since now pattern vacant arch museum tourist brisk come pilot devote"
	const Passphrase = "test"

	ctx := context.Background()
	seedKey := types.NamespacedName{Namespace: Namespace, Name: SeedName}
	secretKey := types.NamespacedName{Namespace: Namespace, Name: SecretName}
	importKey := types.NamespacedName{Namespace: Namespace, Name: ImportSecretName}

	reconcileSeed := func() {
		reconciler := SeedReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: seedKey})
		Expect(err).ToNot(HaveOccurred())
	}

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}})
	})

	AfterEach(func() {
		for _, obj := range []corev1.Secret{
			{ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: Namespace}},
			{ObjectMeta: metav1.ObjectMeta{Name: ImportSecretName, Namespace: Namespace}},
		} {
			_ = k8sClient.Delete(ctx, &obj)
		}
		seed := &bitcoinv1alpha1.Seed{ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace}}
		_ = k8sClient.Delete(ctx, seed)
	})

	It("generates retained seed material and reports Ready", func() {
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName, Network: "simnet"},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		reconcileSeed()

		foundSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, foundSecret)).To(Succeed())
		Expect(strings.Fields(string(foundSecret.Data["mnemonic"]))).To(HaveLen(24))
		Expect(foundSecret.Data["passphrase"]).To(HaveLen(32))
		Expect(foundSecret.Data["rootkey"]).ToNot(BeEmpty())
		Expect(foundSecret.OwnerReferences).To(BeEmpty())
		Expect(foundSecret.Annotations[retainedSeedAnnotation]).To(Equal(SeedName))

		foundSeed := &bitcoinv1alpha1.Seed{}
		Expect(k8sClient.Get(ctx, seedKey, foundSeed)).To(Succeed())
		Expect(foundSeed.Status.SecretName).To(Equal(SecretName))
		Expect(meta.FindStatusCondition(foundSeed.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundSeed.Status.Conditions, "SecretReady").Status).To(Equal(metav1.ConditionTrue))
	})

	DescribeTable("imports seed material from a Secret",
		func(network, hdkey string) {
			input := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: ImportSecretName, Namespace: Namespace},
				StringData: map[string]string{"mnemonic": Mnemonic, "passphrase": Passphrase},
			}
			Expect(k8sClient.Create(ctx, input)).To(Succeed())
			seed := &bitcoinv1alpha1.Seed{
				ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
				Spec: bitcoinv1alpha1.SeedSpec{
					SecretName: SecretName,
					Import: &bitcoinv1alpha1.SeedImport{SecretName: ImportSecretName},
					Network: network,
				},
			}
			Expect(k8sClient.Create(ctx, seed)).To(Succeed())
			reconcileSeed()

			found := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretKey, found)).To(Succeed())
			Expect(found.Data["mnemonic"]).To(Equal([]byte(Mnemonic)))
			Expect(found.Data["passphrase"]).To(Equal([]byte(Passphrase)))
			Expect(found.Data["rootkey"]).To(Equal([]byte(hdkey)))
			Expect(found.OwnerReferences).To(BeEmpty())
		},
		Entry("on simnet", "simnet", "sprv8Erh3X3hFeKunHkJdgLsPuartHeq6F7hf7AbztZnBdVxpxt57x4vLpMB5JYhbryt5Ydn28XYEsMbhW4S1gUJpatAyZqCaco9fsvBfheXzE9"),
		Entry("on mainnet", "mainnet", "xprv9s21ZrQH143K2mhtoUGzSM4Nk8P4oM5CEfmhus3D5fPN6TxDPEtjT8dsBLLdbQFV7kDomWWLYB8M7w8FcAYNomJBKGKKWAtb2WEQcXrtiyY"),
	)

	It("reports invalid import material through status without copying it", func() {
		input := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ImportSecretName, Namespace: Namespace},
			StringData: map[string]string{"mnemonic": "not a valid mnemonic", "passphrase": Passphrase},
		}
		Expect(k8sClient.Create(ctx, input)).To(Succeed())
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{
				SecretName: SecretName,
				Import: &bitcoinv1alpha1.SeedImport{SecretName: ImportSecretName},
			},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		reconcileSeed()

		foundSeed := &bitcoinv1alpha1.Seed{}
		Expect(k8sClient.Get(ctx, seedKey, foundSeed)).To(Succeed())
		ready := meta.FindStatusCondition(foundSeed.Status.Conditions, "Ready")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("InvalidMnemonic"))
		Expect(ready.Message).ToNot(ContainSubstring("not a valid mnemonic"))
		Expect(errors.IsNotFound(k8sClient.Get(ctx, secretKey, &corev1.Secret{}))).To(BeTrue())
	})

	It("refuses to adopt an unrelated target Secret", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: Namespace},
			StringData: map[string]string{"unrelated": "value"},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName},
		})).To(Succeed())
		reconcileSeed()

		foundSeed := &bitcoinv1alpha1.Seed{}
		Expect(k8sClient.Get(ctx, seedKey, foundSeed)).To(Succeed())
		ready := meta.FindStatusCondition(foundSeed.Status.Conditions, "Ready")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("SecretCollision"))
	})

	It("does not silently replace lost generated seed material", func() {
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		reconcileSeed()
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: Namespace}})).To(Succeed())
		reconcileSeed()

		foundSeed := &bitcoinv1alpha1.Seed{}
		Expect(k8sClient.Get(ctx, seedKey, foundSeed)).To(Succeed())
		ready := meta.FindStatusCondition(foundSeed.Status.Conditions, "Ready")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("SeedMaterialLost"))
		Expect(errors.IsNotFound(k8sClient.Get(ctx, secretKey, &corev1.Secret{}))).To(BeTrue())
	})

	It("republishes imported seed material if the retained output Secret is deleted", func() {
		input := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ImportSecretName, Namespace: Namespace},
			StringData: map[string]string{"mnemonic": Mnemonic, "passphrase": Passphrase},
		}
		Expect(k8sClient.Create(ctx, input)).To(Succeed())
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{
				SecretName: SecretName,
				Import: &bitcoinv1alpha1.SeedImport{SecretName: ImportSecretName},
			},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		reconcileSeed()
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: Namespace}})).To(Succeed())
		reconcileSeed()

		found := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, found)).To(Succeed())
		Expect(found.Data["mnemonic"]).To(Equal([]byte(Mnemonic)))
		Expect(found.Data["passphrase"]).To(Equal([]byte(Passphrase)))
	})

	It("rejects Seed derivation configuration changes", func() {
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName, Network: "simnet"},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		found := &bitcoinv1alpha1.Seed{}
		Expect(k8sClient.Get(ctx, seedKey, found)).To(Succeed())
		found.Spec.Network = "mainnet"
		Expect(k8sClient.Update(ctx, found)).ToNot(Succeed())
	})

	It("retains generated seed material after Seed deletion and reuses it on recreation", func() {
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		reconcileSeed()

		found := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, found)).To(Succeed())
		uid := found.UID
		mnemonic := append([]byte(nil), found.Data["mnemonic"]...)

		Expect(k8sClient.Delete(ctx, seed)).To(Succeed())
		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, seedKey, &bitcoinv1alpha1.Seed{}))
		}, time.Minute, time.Second).Should(BeTrue())
		Expect(k8sClient.Get(ctx, secretKey, found)).To(Succeed())
		Expect(found.UID).To(Equal(uid))

		recreated := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName},
		}
		Expect(k8sClient.Create(ctx, recreated)).To(Succeed())
		reconcileSeed()
		Expect(k8sClient.Get(ctx, secretKey, found)).To(Succeed())
		Expect(found.UID).To(Equal(uid))
		Expect(found.Data["mnemonic"]).To(Equal(mnemonic))
	})

	It("migrates a legacy Seed-owned Secret to retained ownership", func() {
		seed := &bitcoinv1alpha1.Seed{
			ObjectMeta: metav1.ObjectMeta{Name: SeedName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.SeedSpec{SecretName: SecretName},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())
		Expect(k8sClient.Get(ctx, seedKey, seed)).To(Succeed())

		controller := true
		legacy := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SecretName,
				Namespace: Namespace,
				Labels:    labelsForSeed(SeedName),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "bitcoin.kiln-fired.github.io/v1alpha1",
					Kind:       "Seed",
					Name:       SeedName,
					UID:        seed.UID,
					Controller: &controller,
				}},
			},
			StringData: map[string]string{"mnemonic": Mnemonic, "passphrase": Passphrase, "rootkey": "legacy-root"},
		}
		Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
		reconcileSeed()

		found := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, found)).To(Succeed())
		Expect(found.OwnerReferences).To(BeEmpty())
		Expect(found.Annotations[retainedSeedAnnotation]).To(Equal(SeedName))
		Expect(found.Data["rootkey"]).To(Equal([]byte("legacy-root")))
	})
})
