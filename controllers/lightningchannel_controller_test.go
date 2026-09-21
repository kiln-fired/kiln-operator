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

var _ = Describe("LightningChannel controller", func() {
	const nodeName = "alice"
	const peerName = "bob"
	const channelName = "alice-bob"
	const pubkey = "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	ctx := context.Background()
	var namespace string
	var channelKey types.NamespacedName

	createDependencies := func(network string) (*bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer) {
		node := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: namespace},
			Spec: bitcoinv1alpha1.LightningNodeSpec{
				BitcoinConnection: bitcoinv1alpha1.BitcoinConnection{External: &bitcoinv1alpha1.ExternalBitcoinConnection{
					Host:                 "btcd",
					Network:              network,
					CertSecret:           "btcd-rpc-tls",
					ApiAuthSecretName:    "btcd-rpc-creds",
					ApiUserSecretKey:     "username",
					ApiPasswordSecretKey: "password",
				}},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		node.Status.Network = network
		node.Status.Phase = "Ready"
		node.Status.Runtime.SyncedToChain = true
		node.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "RPCReady",
			Message: "ready", LastTransitionTime: metav1.Now(),
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

		peer := &bitcoinv1alpha1.LightningPeer{
			ObjectMeta: metav1.ObjectMeta{Name: peerName, Namespace: namespace},
			Spec: bitcoinv1alpha1.LightningPeerSpec{
				NodeRef: nodeName,
				Pubkey:  pubkey,
				Address: "bob.example.com:9735",
			},
		}
		Expect(k8sClient.Create(ctx, peer)).To(Succeed())
		peer.Status.Phase = "Connected"
		peer.Status.Connected = true
		peer.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "PeerConnected",
			Message: "ready", LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, peer)).To(Succeed())
		return node, peer
	}

	createChannel := func(allowMainnet bool) *bitcoinv1alpha1.LightningChannel {
		channel := &bitcoinv1alpha1.LightningChannel{
			ObjectMeta: metav1.ObjectMeta{Name: channelName, Namespace: namespace},
			Spec: bitcoinv1alpha1.LightningChannelSpec{
				PeerRef:      peerName,
				CapacitySats: 100000,
				Private:      true,
				MinConfs:     1,
				Safety: bitcoinv1alpha1.NetworkSafetyPolicy{
					AllowMainnet: allowMainnet,
				},
			},
		}
		Expect(k8sClient.Create(ctx, channel)).To(Succeed())
		return channel
	}

	BeforeEach(func() {
		namespace = fmt.Sprintf("test-lightning-channel-%d", time.Now().UnixNano())
		channelKey = types.NamespacedName{Namespace: namespace, Name: channelName}
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	AfterEach(func() {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns); err == nil {
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
		}
	})

	It("opens a missing channel once and then observes pending state without funding again", func() {
		createDependencies("simnet")
		createChannel(false)

		state := "Missing"
		openCalls := 0
		reconciler := LightningChannelReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ObserveChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *corev1.Secret) (*LightningChannelObservation, error) {
				if state == "PendingOpen" {
					return &LightningChannelObservation{
						State: "PendingOpen", ChannelPoint: "abc123:0",
						RemotePubkey: pubkey, CapacitySats: 100000, Private: true,
					}, nil
				}
				return &LightningChannelObservation{State: "Missing"}, nil
			},
			OpenChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningChannelOpenResult, error) {
				openCalls++
				state = "PendingOpen"
				return &LightningChannelOpenResult{ChannelPoint: "abc123:0"}, nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(openCalls).To(Equal(1))

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(openCalls).To(Equal(1))

		found := &bitcoinv1alpha1.LightningChannel{}
		Expect(k8sClient.Get(ctx, channelKey, found)).To(Succeed())
		Expect(found.Finalizers).To(ContainElement(lightningChannelFinalizer))
		Expect(found.Status.Phase).To(Equal("PendingOpen"))
		Expect(found.Status.ChannelPoint).To(Equal("abc123:0"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Funded").Status).To(Equal(metav1.ConditionTrue))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionFalse))
	})

	It("adopts its memo-tagged open channel without issuing a funding request", func() {
		createDependencies("simnet")
		createChannel(false)

		openCalls := 0
		reconciler := LightningChannelReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ObserveChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *corev1.Secret) (*LightningChannelObservation, error) {
				return &LightningChannelObservation{
					State: "Open", ChannelPoint: "def456:1", RemotePubkey: pubkey,
					Active: true, CapacitySats: 100000, LocalBalanceSats: 95000,
					RemoteBalanceSats: 5000, Private: true,
				}, nil
			},
			OpenChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningChannelOpenResult, error) {
				openCalls++
				return nil, nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(openCalls).To(BeZero())

		found := &bitcoinv1alpha1.LightningChannel{}
		Expect(k8sClient.Get(ctx, channelKey, found)).To(Succeed())
		Expect(found.Status.Phase).To(Equal("Open"))
		Expect(found.Status.Active).To(BeTrue())
		Expect(found.Status.ChannelPoint).To(Equal("def456:1"))
		Expect(found.Status.LocalBalanceSats).To(Equal(int64(95000)))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
	})

	It("waits for a connected LightningPeer before funding", func() {
		_, peer := createDependencies("simnet")
		peer.Status.Connected = false
		peer.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "Connecting",
			Message: "connecting", LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, peer)).To(Succeed())
		createChannel(false)

		openCalls := 0
		reconciler := LightningChannelReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			OpenChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningChannelOpenResult, error) {
				openCalls++
				return nil, nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(openCalls).To(BeZero())

		found := &bitcoinv1alpha1.LightningChannel{}
		Expect(k8sClient.Get(ctx, channelKey, found)).To(Succeed())
		Expect(found.Status.Phase).To(Equal("WaitingForDependency"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "PeerReady").Reason).To(Equal("LightningPeerNotReady"))
	})

	It("blocks mainnet funding unless the channel explicitly opts in", func() {
		createDependencies("mainnet")
		createChannel(false)

		openCalls := 0
		reconciler := LightningChannelReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			OpenChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningChannelOpenResult, error) {
				openCalls++
				return nil, nil
			},
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(openCalls).To(BeZero())

		found := &bitcoinv1alpha1.LightningChannel{}
		Expect(k8sClient.Get(ctx, channelKey, found)).To(Succeed())
		Expect(found.Status.Phase).To(Equal("NetworkBlocked"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NetworkReady").Reason).To(Equal("MainnetOptInRequired"))
	})

	It("cooperatively closes before releasing the finalizer", func() {
		createDependencies("simnet")
		channel := createChannel(false)
		channel.Finalizers = []string{lightningChannelFinalizer}
		Expect(k8sClient.Update(ctx, channel)).To(Succeed())

		state := "Open"
		closeCalls := 0
		reconciler := LightningChannelReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			ObserveChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *corev1.Secret) (*LightningChannelObservation, error) {
				if state == "Open" {
					return &LightningChannelObservation{State: "Open", ChannelPoint: "closeme:0", RemotePubkey: pubkey}, nil
				}
				return &LightningChannelObservation{State: "Missing"}, nil
			},
			CloseChannel: func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *corev1.Secret, string) error {
				closeCalls++
				state = "Missing"
				return nil
			},
		}

		Expect(k8sClient.Delete(ctx, channel)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(closeCalls).To(Equal(1))

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: channelKey})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			err := k8sClient.Get(ctx, channelKey, &bitcoinv1alpha1.LightningChannel{})
			return errors.IsNotFound(err)
		}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
	})
})
