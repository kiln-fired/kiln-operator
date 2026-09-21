package controllers

import (
	"context"
	"fmt"
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

var _ = Describe("LightningPeer controller", func() {
	const nodeName = "alice"
	const peerName = "bob"
	const pubkey = "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const address = "bob.example.com:9735"

	ctx := context.Background()
	var namespace string
	var peerKey types.NamespacedName

	createReadyNode := func() *bitcoinv1alpha1.LightningNode {
		node := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: namespace},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		node.Status.Phase = "Ready"
		node.Status.Runtime.SyncedToChain = true
		node.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "RPCReady",
			Message:            "ready",
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: lightningOperatorRPCSecretName(node), Namespace: namespace},
			Data: map[string][]byte{
				"tls.cert":       []byte("tls"),
				"admin.macaroon": []byte("admin"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		return node
	}

	createPeer := func() *bitcoinv1alpha1.LightningPeer {
		peer := &bitcoinv1alpha1.LightningPeer{
			ObjectMeta: metav1.ObjectMeta{Name: peerName, Namespace: namespace},
			Spec: bitcoinv1alpha1.LightningPeerSpec{
				NodeRef: nodeName,
				Pubkey:  pubkey,
				Address: address,
			},
		}
		Expect(k8sClient.Create(ctx, peer)).To(Succeed())
		return peer
	}

	BeforeEach(func() {
		namespace = fmt.Sprintf("test-lightning-peer-%d", time.Now().UnixNano())
		peerKey = types.NamespacedName{Namespace: namespace, Name: peerName}
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	AfterEach(func() {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns); err == nil {
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
		}
	})

	It("connects an absent peer once and then observes desired state", func() {
		createReadyNode()
		createPeer()

		connected := false
		connectCalls := 0
		reconciler := LightningPeerReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ObservePeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningPeerObservation, error) {
				return &LightningPeerObservation{Connected: connected, Address: address}, nil
			},
			ConnectPeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error {
				connectCalls++
				connected = true
				return nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: peerKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(connectCalls).To(Equal(1))

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: peerKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(connectCalls).To(Equal(1))

		found := &bitcoinv1alpha1.LightningPeer{}
		Expect(k8sClient.Get(ctx, peerKey, found)).To(Succeed())
		Expect(found.Finalizers).To(ContainElement(lightningPeerFinalizer))
		Expect(found.Status.Phase).To(Equal("Connected"))
		Expect(found.Status.Connected).To(BeTrue())
		Expect(found.Status.ObservedAddress).To(Equal(address))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NodeReady").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Connected").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
	})

	It("does not issue a connect command when the peer already exists", func() {
		createReadyNode()
		createPeer()

		connectCalls := 0
		reconciler := LightningPeerReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ObservePeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningPeerObservation, error) {
				return &LightningPeerObservation{Connected: true, Address: "203.0.113.8:9735", Inbound: true}, nil
			},
			ConnectPeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error {
				connectCalls++
				return nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: peerKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(connectCalls).To(BeZero())

		found := &bitcoinv1alpha1.LightningPeer{}
		Expect(k8sClient.Get(ctx, peerKey, found)).To(Succeed())
		Expect(found.Status.Connected).To(BeTrue())
		Expect(found.Status.Inbound).To(BeTrue())
		Expect(found.Status.ObservedAddress).To(Equal("203.0.113.8:9735"))
	})

	It("waits for the referenced LightningNode instead of attempting a connection", func() {
		createPeer()
		connectCalls := 0
		reconciler := LightningPeerReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ConnectPeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error {
				connectCalls++
				return nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: peerKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(connectCalls).To(BeZero())

		found := &bitcoinv1alpha1.LightningPeer{}
		Expect(k8sClient.Get(ctx, peerKey, found)).To(Succeed())
		Expect(found.Status.Phase).To(Equal("WaitingForNode"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Reason).To(Equal("LightningNodeNotFound"))
	})

	It("disconnects the peer before allowing the resource to disappear", func() {
		createReadyNode()
		peer := createPeer()
		peer.Finalizers = []string{lightningPeerFinalizer}
		Expect(k8sClient.Update(ctx, peer)).To(Succeed())

		disconnectCalls := 0
		reconciler := LightningPeerReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ObservePeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningPeerObservation, error) {
				return &LightningPeerObservation{Connected: true, Address: address}, nil
			},
			DisconnectPeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error {
				disconnectCalls++
				return nil
			},
		}

		Expect(k8sClient.Delete(ctx, peer)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: peerKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(disconnectCalls).To(Equal(1))

		Eventually(func() bool {
			err := k8sClient.Get(ctx, peerKey, &bitcoinv1alpha1.LightningPeer{})
			return errors.IsNotFound(err)
		}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
	})
	It("waits for chain sync before attempting a peer connection", func() {
		node := createReadyNode()
		node.Status.Runtime.SyncedToChain = false
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		createPeer()

		connectCalls := 0
		reconciler := LightningPeerReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ConnectPeer: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error {
				connectCalls++
				return nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: peerKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(connectCalls).To(BeZero())

		found := &bitcoinv1alpha1.LightningPeer{}
		Expect(k8sClient.Get(ctx, peerKey, found)).To(Succeed())
		Expect(found.Status.Phase).To(Equal("WaitingForNode"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NodeReady").Reason).To(Equal("LightningNodeChainNotSynced"))
	})

})
