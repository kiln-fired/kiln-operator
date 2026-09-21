package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("LightningNode controller", func() {
	const Namespace = "test-lightning-namespace"
	const LightningNodeName = "test-lightning"

	ctx := context.Background()
	lightningNodeNamespaceName := types.NamespacedName{Namespace: Namespace, Name: LightningNodeName}

	var reconciler LightningNodeReconciler

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}})
		reconciler = LightningNodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	AfterEach(func() {
		lightningNode := &bitcoinv1alpha1.LightningNode{}
		if err := k8sClient.Get(ctx, lightningNodeNamespaceName, lightningNode); err == nil {
			_ = k8sClient.Delete(ctx, lightningNode)
			Eventually(func() bool {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
				err := k8sClient.Get(ctx, lightningNodeNamespaceName, &bitcoinv1alpha1.LightningNode{})
				return errors.IsNotFound(err)
			}, time.Minute, 100*time.Millisecond).Should(BeTrue())
		}
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}})
	})

	It("preserves Lightning state and reports lifecycle status", func() {
		lightningNode := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: LightningNodeName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.LightningNodeSpec{
				BitcoinConnection: bitcoinv1alpha1.BitcoinConnection{
					Host:                 "btcd",
					Network:              "simnet",
					CertSecret:           "btcd-rpc-tls",
					ApiAuthSecretName:    "btcd-rpc-creds",
					ApiUserSecretKey:     "username",
					ApiPasswordSecretKey: "password",
				},
				Wallet: bitcoinv1alpha1.Wallet{
					Password: bitcoinv1alpha1.WalletPassword{SecretName: "alice-wallet", SecretKey: "password"},
					Seed:     bitcoinv1alpha1.SeedImport{SecretName: "seed"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, lightningNode)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		foundLightningNode := &bitcoinv1alpha1.LightningNode{}
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, foundLightningNode)).To(Succeed())
		Expect(foundLightningNode.Finalizers).To(ContainElement(lightningNodeFinalizer))
		Expect(foundLightningNode.Status.Phase).To(Equal("Initializing"))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "BitcoinReady").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "StorageFenced").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionFalse))

		statefulSet := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet)).To(Succeed())
		Expect(statefulSet.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
		Expect(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy).ToNot(BeNil())
		Expect(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		Expect(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		Expect(statefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds).ToNot(BeNil())
		Expect(*statefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(60)))
		Expect(statefulSet.Spec.VolumeClaimTemplates).To(HaveLen(1))
		Expect(statefulSet.Spec.VolumeClaimTemplates[0].Name).To(Equal("lnd-data"))
		Expect(statefulSet.Spec.VolumeClaimTemplates[0].Spec.AccessModes).To(Equal([]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}))

		Expect(statefulSet.Spec.Template.Spec.InitContainers).To(HaveLen(1))
		Expect(statefulSet.Spec.Template.Spec.InitContainers[0].Image).To(Equal("docker.io/lightninglabs/lndinit:v0.1.36-beta-lnd-v0.21.0-beta"))
		Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(1))
		lnd := statefulSet.Spec.Template.Spec.Containers[0]
		Expect(lnd.Image).To(Equal("docker.io/lightninglabs/lnd:v0.21.0-beta"))
		Expect(lnd.Args).To(ContainElements(
			"--lnddir=/data",
			"--wallet-unlock-password-file=/secret/wallet-password",
			"--bitcoin.active",
			"--bitcoin.$(NETWORK)",
			"--bitcoin.node=btcd",
		))
		Expect(lnd.Lifecycle).ToNot(BeNil())
		Expect(lnd.Lifecycle.PreStop).ToNot(BeNil())
		Expect(lnd.Lifecycle.PreStop.Exec.Command[2]).To(ContainSubstring("lncli"))
		Expect(lnd.Lifecycle.PreStop.Exec.Command[2]).To(ContainSubstring(" stop"))
		Expect(lnd.ReadinessProbe).ToNot(BeNil())
		Expect(lnd.SecurityContext.RunAsUser).ToNot(BeNil())
		Expect(*lnd.SecurityContext.RunAsUser).To(Equal(int64(65532)))
		Expect(statefulSet.Spec.Template.Spec.SecurityContext.FSGroup).ToNot(BeNil())
		Expect(*statefulSet.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(int64(65532)))

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet)).To(Succeed())
		statefulSet.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, statefulSet)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, foundLightningNode)).To(Succeed())
		Expect(foundLightningNode.Status.Phase).To(Equal("Ready"))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "WalletReady").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
	})

	It("derives RPC connection details from a ready BitcoinNode", func() {
		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{Name: "bitcoin", Namespace: Namespace},
			Spec: bitcoinv1alpha1.BitcoinNodeSpec{RPCServer: bitcoinv1alpha1.RPCServer{
				CertSecret:           "bitcoin-tls",
				ApiAuthSecretName:    "bitcoin-creds",
				ApiUserSecretKey:     "user",
				ApiPasswordSecretKey: "pass",
			}},
		}
		Expect(k8sClient.Create(ctx, bitcoinNode)).To(Succeed())
		bitcoinNode.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "RPCReady", LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, bitcoinNode)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, bitcoinNode) })

		lightningNode := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: LightningNodeName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.LightningNodeSpec{
				BitcoinConnection: bitcoinv1alpha1.BitcoinConnection{NodeRef: "bitcoin", Network: "simnet"},
				Wallet: bitcoinv1alpha1.Wallet{
					Password: bitcoinv1alpha1.WalletPassword{SecretName: "wallet", SecretKey: "password"},
					Seed:     bitcoinv1alpha1.SeedImport{SecretName: "seed"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, lightningNode)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		statefulSet := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet)).To(Succeed())
		lnd := statefulSet.Spec.Template.Spec.Containers[0]

		var rpcHost string
		var rpcSecret string
		for _, env := range lnd.Env {
			switch env.Name {
			case "RPCHOST":
				rpcHost = env.Value
			case "RPCUSER":
				rpcSecret = env.ValueFrom.SecretKeyRef.Name
			}
		}
		Expect(rpcHost).To(Equal("bitcoin"))
		Expect(rpcSecret).To(Equal("bitcoin-creds"))

		foundLightningNode := &bitcoinv1alpha1.LightningNode{}
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, foundLightningNode)).To(Succeed())
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "BitcoinReady").Reason).To(Equal("BitcoinNodeReady"))
	})
})
