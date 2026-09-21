package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
		reconciler = LightningNodeReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			GetInfo: func(context.Context, *bitcoinv1alpha1.LightningNode, *corev1.Secret) (*bitcoinv1alpha1.LightningRuntimeStatus, error) {
				return &bitcoinv1alpha1.LightningRuntimeStatus{
					IdentityPubkey:      "02kiln",
					Alias:               "kiln-e2e",
					Version:             "0.21.0-beta",
					BlockHeight:         321,
					SyncedToChain:       true,
					SyncedToGraph:       true,
					NumPeers:            2,
					NumPendingChannels:  1,
					NumActiveChannels:   3,
					NumInactiveChannels: 4,
				}, nil
			},
		}
	})

	AfterEach(func() {
		lightningNode := &bitcoinv1alpha1.LightningNode{}
		if err := k8sClient.Get(ctx, lightningNodeNamespaceName, lightningNode); err == nil {
			Expect(k8sClient.Delete(ctx, lightningNode)).To(Succeed())

			statefulSet := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet); err == nil {
				Expect(k8sClient.Delete(ctx, statefulSet)).To(Succeed())
			}
			service := &corev1.Service{}
			if err := k8sClient.Get(ctx, lightningNodeNamespaceName, service); err == nil {
				Expect(k8sClient.Delete(ctx, service)).To(Succeed())
			}

			rpcSecret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: lightningRPCSecretName(lightningNode), Namespace: Namespace}, rpcSecret); err == nil {
				Expect(k8sClient.Delete(ctx, rpcSecret)).To(Succeed())
			}
			publisherName := lightningRPCPublisherName(lightningNode)
			serviceAccount := &corev1.ServiceAccount{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: Namespace}, serviceAccount); err == nil {
				Expect(k8sClient.Delete(ctx, serviceAccount)).To(Succeed())
			}
			role := &rbacv1.Role{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: Namespace}, role); err == nil {
				Expect(k8sClient.Delete(ctx, role)).To(Succeed())
			}
			roleBinding := &rbacv1.RoleBinding{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: Namespace}, roleBinding); err == nil {
				Expect(k8sClient.Delete(ctx, roleBinding)).To(Succeed())
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, lightningNodeNamespaceName, &bitcoinv1alpha1.LightningNode{})
				return errors.IsNotFound(err)
			}, time.Minute, time.Second).Should(BeTrue())
		}
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
		Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(2))
		lnd := statefulSet.Spec.Template.Spec.Containers[0]
		publisher := statefulSet.Spec.Template.Spec.Containers[1]
		Expect(publisher.Name).To(Equal("rpc-credential-publisher"))
		Expect(publisher.Image).To(Equal("docker.io/lightninglabs/lndinit:v0.1.36-beta-lnd-v0.21.0-beta"))
		Expect(publisher.Args).To(HaveLen(1))
		Expect(publisher.Args[0]).To(ContainSubstring("readonly.macaroon"))
		Expect(publisher.Args[0]).To(ContainSubstring("invoice.macaroon"))
		Expect(publisher.Args[0]).To(ContainSubstring("admin.macaroon"))
		Expect(publisher.Args[0]).To(ContainSubstring("OPERATOR_RPC_SECRET_NAME"))
		Expect(statefulSet.Spec.Template.Spec.ServiceAccountName).To(Equal(lightningRPCPublisherName(lightningNode)))
		Expect(lnd.Image).To(Equal("docker.io/lightninglabs/lnd:v0.21.0-beta"))
		Expect(lnd.Args).To(ContainElements(
			"--lnddir=/data",
			"--wallet-unlock-password-file=/secret/wallet-password",
			"--bitcoin.active",
			"--bitcoin.$(NETWORK)",
			"--bitcoin.node=btcd",
			"--tlsextradomain=$(RPCSERVICE)",
			"--tlsdisableautofill",
			"--tlsautorefresh",
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

		rpcSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lightningRPCSecretName(lightningNode), Namespace: Namespace}, rpcSecret)).To(Succeed())
		Expect(rpcSecret.Data).To(BeEmpty())

		operatorSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lightningOperatorRPCSecretName(lightningNode), Namespace: Namespace}, operatorSecret)).To(Succeed())
		Expect(operatorSecret.Data).To(BeEmpty())
		Expect(operatorSecret.Annotations["bitcoin.kiln-fired.github.io/internal"]).To(Equal("true"))

		publisherName := lightningRPCPublisherName(lightningNode)
		serviceAccount := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: Namespace}, serviceAccount)).To(Succeed())
		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: Namespace}, role)).To(Succeed())
		Expect(role.Rules).To(HaveLen(1))
		Expect(role.Rules[0].ResourceNames).To(ConsistOf(
			lightningRPCSecretName(lightningNode),
			lightningOperatorRPCSecretName(lightningNode),
		))
		Expect(role.Rules[0].Verbs).To(ConsistOf("get", "update", "patch"))
		roleBinding := &rbacv1.RoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: Namespace}, roleBinding)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet)).To(Succeed())
		statefulSet.Status.Replicas = 1
		statefulSet.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, statefulSet)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, foundLightningNode)).To(Succeed())
		Expect(foundLightningNode.Status.Phase).To(Equal("PublishingCredentials"))
		Expect(foundLightningNode.Status.RPCSecretName).To(Equal(lightningRPCSecretName(lightningNode)))
		Expect(foundLightningNode.Status.RPCAddress).To(Equal("test-lightning.test-lightning-namespace.svc.cluster.local:10009"))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "CredentialsReady").Status).To(Equal(metav1.ConditionFalse))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionFalse))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lightningRPCSecretName(lightningNode), Namespace: Namespace}, rpcSecret)).To(Succeed())
		rpcSecret.Data = map[string][]byte{
			"tls.cert":         []byte("tls"),
			"readonly.macaroon": []byte("readonly"),
			"invoice.macaroon":  []byte("invoice"),
		}
		Expect(k8sClient.Update(ctx, rpcSecret)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, foundLightningNode)).To(Succeed())
		Expect(foundLightningNode.Status.Phase).To(Equal("Ready"))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "WalletReady").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "CredentialsReady").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "RuntimeReady").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(foundLightningNode.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
		Expect(foundLightningNode.Status.Runtime.IdentityPubkey).To(Equal("02kiln"))
		Expect(foundLightningNode.Status.Runtime.Version).To(Equal("0.21.0-beta"))
		Expect(foundLightningNode.Status.Runtime.BlockHeight).To(Equal(uint32(321)))
		Expect(foundLightningNode.Status.Runtime.SyncedToChain).To(BeTrue())
		Expect(foundLightningNode.Status.Runtime.NumActiveChannels).To(Equal(uint32(3)))
	})

	It("refuses to grant publisher access to an unrelated Secret", func() {
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
					Password: bitcoinv1alpha1.WalletPassword{SecretName: "wallet", SecretKey: "password"},
					Seed:     bitcoinv1alpha1.SeedImport{SecretName: "seed"},
				},
			},
		}
		unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: lightningRPCSecretName(lightningNode), Namespace: Namespace,
		}}
		Expect(k8sClient.Create(ctx, unrelated)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, unrelated) })

		Expect(k8sClient.Create(ctx, lightningNode)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("is not controlled by LightningNode"))
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
	It("blocks mainnet Lightning without explicit opt-in", func() {
		lightningNode := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: LightningNodeName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.LightningNodeSpec{
				BitcoinConnection: bitcoinv1alpha1.BitcoinConnection{
					Host:                 "btcd",
					Network:              "mainnet",
					CertSecret:           "btcd-rpc-tls",
					ApiAuthSecretName:    "btcd-rpc-creds",
					ApiUserSecretKey:     "username",
					ApiPasswordSecretKey: "password",
				},
			},
		}
		Expect(k8sClient.Create(ctx, lightningNode)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		found := &bitcoinv1alpha1.LightningNode{}
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, found)).To(Succeed())
		Expect(found.Status.Network).To(Equal("mainnet"))
		Expect(found.Status.Phase).To(Equal("NetworkBlocked"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NetworkReady").Reason).To(Equal("MainnetOptInRequired"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionFalse))

		statefulSet := &appsv1.StatefulSet{}
		err = k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("blocks a LightningNode whose network does not match its BitcoinNode", func() {
		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{Name: "bitcoin-mismatch", Namespace: Namespace},
			Spec: bitcoinv1alpha1.BitcoinNodeSpec{Network: "testnet"},
		}
		Expect(k8sClient.Create(ctx, bitcoinNode)).To(Succeed())
		bitcoinNode.Status.Network = "testnet"
		bitcoinNode.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "RPCReady", LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, bitcoinNode)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, bitcoinNode) })

		lightningNode := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: LightningNodeName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.LightningNodeSpec{
				BitcoinConnection: bitcoinv1alpha1.BitcoinConnection{NodeRef: "bitcoin-mismatch", Network: "simnet"},
			},
		}
		Expect(k8sClient.Create(ctx, lightningNode)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lightningNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		found := &bitcoinv1alpha1.LightningNode{}
		Expect(k8sClient.Get(ctx, lightningNodeNamespaceName, found)).To(Succeed())
		Expect(found.Status.Phase).To(Equal("NetworkBlocked"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NetworkReady").Reason).To(Equal("NetworkMismatch"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Reason).To(Equal("NetworkMismatch"))

		statefulSet := &appsv1.StatefulSet{}
		err = k8sClient.Get(ctx, lightningNodeNamespaceName, statefulSet)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

})
