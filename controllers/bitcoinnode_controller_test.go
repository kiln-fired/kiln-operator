package controllers

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("BitcoinNode controller", func() {

	const Namespace = "test-namespace"
	const BitcoinNodeName = "test"

	ctx := context.Background()
	bitcoinNodeNamespaceName := types.NamespacedName{Namespace: Namespace, Name: BitcoinNodeName}
	statefulSetNamespaceName := types.NamespacedName{Namespace: Namespace, Name: BitcoinNodeName}

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
		By("cleaning up BitcoingNode")
		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{}
		err := k8sClient.Get(ctx, bitcoinNodeNamespaceName, bitcoinNode)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, bitcoinNode)
		Expect(err).To(Not(HaveOccurred()))

		By("cleaning up StatefulSet")
		statefulSet := &appsv1.StatefulSet{}
		err = k8sClient.Get(ctx, statefulSetNamespaceName, statefulSet)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, statefulSet)
		Expect(err).To(Not(HaveOccurred()))
	})

	It("should reconcile the BitcoinNode instance", func() {

		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{
				Name:      BitcoinNodeName,
				Namespace: Namespace,
			},
			Spec: bitcoinv1alpha1.BitcoinNodeSpec{
				Mining: bitcoinv1alpha1.Mining{
					CpuMiningEnabled: false,
					RewardAddress: bitcoinv1alpha1.RewardAddress{
						SecretName: "seed",
					},
					MinBlocks:             400,
					PeriodicBlocksEnabled: true,
					SecondsPerBlock:       10,
				},
				RPCServer: bitcoinv1alpha1.RPCServer{
					CertSecret:           "btcd-rpc-tls",
					ApiAuthSecretName:    "btcd-rpc-creds",
					ApiUserSecretKey:     "username",
					ApiPasswordSecretKey: "password",
				},
			},
		}

		By("creating the custom resource for the kind BitcoinNode")
		err := k8sClient.Create(ctx, bitcoinNode)
		Expect(err).To(Not(HaveOccurred()))

		By("checking if the custom resource was successfully created")
		Eventually(func() error {
			foundBitcoinNode := &bitcoinv1alpha1.BitcoinNode{}
			return k8sClient.Get(ctx, bitcoinNodeNamespaceName, foundBitcoinNode)
		}, time.Minute, time.Second).Should(Succeed())

		By("reconciling the custom resource created")
		bitcoinNodeReconciler := BitcoinNodeReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		_, err = bitcoinNodeReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: bitcoinNodeNamespaceName,
		})
		Expect(err).To(Not(HaveOccurred()))

		By("checking if a statefulset was successfully created in the reconciliation")
		foundStatefulSet := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, statefulSetNamespaceName, foundStatefulSet)
		}, time.Minute, time.Second).Should(Succeed())

		By("checking if the mining address is the expected secret reference")
		Eventually(func() error {
			miningAddressEnvExists := false
			for _, container := range foundStatefulSet.Spec.Template.Spec.Containers {
				if container.Name == "btcd" {
					for _, env := range container.Env {
						if env.Name == "MINING_ADDRESS" {
							miningAddressEnvExists = true
							Expect(env.ValueFrom).To(Not(BeNil()))
							Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Not(BeEmpty()))
							Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Equal(bitcoinNode.Spec.Mining.RewardAddress.SecretName))
							Expect(env.ValueFrom.SecretKeyRef.Key).To(Not(BeEmpty()))
							Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal(bitcoinNode.Spec.Mining.RewardAddress.SecretKey))
						}
					}
				}
			}
			Expect(miningAddressEnvExists).To(BeTrue())
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("checking if the rpc credentials are the expected secret references")
		Eventually(func() error {
			rpcUserEnvExists := false
			rpcPassEnvExists := false
			for _, container := range foundStatefulSet.Spec.Template.Spec.Containers {
				if container.Name == "btcd" {
					for _, env := range container.Env {
						if env.Name == "RPCUSER" {
							rpcUserEnvExists = true
							Expect(env.ValueFrom).To(Not(BeNil()))
							Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Not(BeEmpty()))
							Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Equal(bitcoinNode.Spec.RPCServer.ApiAuthSecretName))
							Expect(env.ValueFrom.SecretKeyRef.Key).To(Not(BeEmpty()))
							Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal(bitcoinNode.Spec.RPCServer.ApiUserSecretKey))
						}
						if env.Name == "RPCPASS" {
							rpcPassEnvExists = true
							Expect(env.ValueFrom).To(Not(BeNil()))
							Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Not(BeEmpty()))
							Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Equal(bitcoinNode.Spec.RPCServer.ApiAuthSecretName))
							Expect(env.ValueFrom.SecretKeyRef.Key).To(Not(BeEmpty()))
							Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal(bitcoinNode.Spec.RPCServer.ApiPasswordSecretKey))
						}
					}
				}
			}
			Expect(rpcUserEnvExists).To(BeTrue())
			Expect(rpcPassEnvExists).To(BeTrue())
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("checking for the existence of a timer container")
		Eventually(func() error {
			Expect(len(foundStatefulSet.Spec.Template.Spec.Containers)).To(Equal(2))
			return nil
		}, time.Minute, time.Second).Should(Succeed())
	})

})
