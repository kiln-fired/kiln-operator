package controllers

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	statefulSetNamespaceName := types.NamespacedName{Namespace: Namespace, Name: bitcoinNodeOwnedResourceName(BitcoinNodeName)}

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
		By("cleaning up BitcoinNode through its finalizer")
		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{}
		err := k8sClient.Get(ctx, bitcoinNodeNamespaceName, bitcoinNode)
		if err == nil {
			Expect(k8sClient.Delete(ctx, bitcoinNode)).To(Succeed())

			statefulSet := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, statefulSetNamespaceName, statefulSet); err == nil {
				Expect(k8sClient.Delete(ctx, statefulSet)).To(Succeed())
			}

			reconciler := BitcoinNodeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: bitcoinNodeNamespaceName,
			})
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, bitcoinNodeNamespaceName, &bitcoinv1alpha1.BitcoinNode{})
				return errors.IsNotFound(err)
			}, time.Minute, time.Second).Should(BeTrue())
		}
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

		By("checking lifecycle finalization defaults")
		foundBitcoinNode := &bitcoinv1alpha1.BitcoinNode{}
		Expect(k8sClient.Get(ctx, bitcoinNodeNamespaceName, foundBitcoinNode)).To(Succeed())
		Expect(foundBitcoinNode.Finalizers).To(ContainElement(bitcoinNodeFinalizer))
		btcdLifecycle := foundStatefulSet.Spec.Template.Spec.Containers[0].Lifecycle
		Expect(btcdLifecycle).ToNot(BeNil())
		Expect(btcdLifecycle.PreStop).ToNot(BeNil())
		Expect(btcdLifecycle.PreStop.Exec).ToNot(BeNil())
		Expect(btcdLifecycle.PreStop.Exec.Command).To(HaveLen(3))
		Expect(btcdLifecycle.PreStop.Exec.Command[2]).To(ContainSubstring("btcctl"))
		Expect(btcdLifecycle.PreStop.Exec.Command[2]).To(ContainSubstring(" stop"))

		By("checking stateful safety defaults")
		Expect(foundStatefulSet.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
		Expect(foundStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy).ToNot(BeNil())
		Expect(foundStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		Expect(foundStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		Expect(foundStatefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds).ToNot(BeNil())
		Expect(*foundStatefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(60)))
		Expect(foundStatefulSet.Spec.VolumeClaimTemplates).To(HaveLen(1))
		Expect(foundStatefulSet.Spec.VolumeClaimTemplates[0].Spec.AccessModes).To(Equal([]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}))

		By("checking the upstream btcd runtime configuration")
		Expect(foundStatefulSet.Spec.Template.Spec.Containers).To(HaveLen(2))
		btcdContainer := foundStatefulSet.Spec.Template.Spec.Containers[0]
		Expect(btcdContainer.Name).To(Equal("btcd"))
		Expect(btcdContainer.Image).To(Equal("ghcr.io/btcsuite/btcd:v0.26.2"))
		Expect(btcdContainer.Command).To(Equal([]string{"btcd"}))
		Expect(btcdContainer.Args).To(ContainElements(
			"--simnet",
			"--listen=0.0.0.0:18555",
			"--rpclisten=0.0.0.0:18556",
			"--rpccert=/rpc/rpc.cert",
			"--rpckey=/rpc/rpc.key",
			"--datadir=/data",
			"--logdir=/data/logs",
		))
		Expect(foundStatefulSet.Spec.Template.Spec.SecurityContext).ToNot(BeNil())
		Expect(foundStatefulSet.Spec.Template.Spec.SecurityContext.FSGroup).ToNot(BeNil())
		Expect(*foundStatefulSet.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(int64(65532)))
		Expect(btcdContainer.SecurityContext).ToNot(BeNil())
		Expect(btcdContainer.SecurityContext.RunAsUser).ToNot(BeNil())
		Expect(*btcdContainer.SecurityContext.RunAsUser).To(Equal(int64(65532)))
		Expect(btcdContainer.SecurityContext.RunAsGroup).ToNot(BeNil())
		Expect(*btcdContainer.SecurityContext.RunAsGroup).To(Equal(int64(65532)))
		Expect(btcdContainer.LivenessProbe.Exec.Command).To(HaveLen(3))
		Expect(btcdContainer.LivenessProbe.Exec.Command[0:2]).To(Equal([]string{"/bin/sh", "-c"}))
		Expect(btcdContainer.LivenessProbe.Exec.Command[2]).To(ContainSubstring("--configfile=/dev/null"))
		Expect(btcdContainer.LivenessProbe.Exec.Command[2]).To(ContainSubstring("--rpcserver=\"$RPCSERVER\""))
		Expect(btcdContainer.ReadinessProbe.Exec.Command).To(HaveLen(3))
		Expect(btcdContainer.ReadinessProbe.Exec.Command[0:2]).To(Equal([]string{"/bin/sh", "-c"}))
		Expect(btcdContainer.ReadinessProbe.Exec.Command[2]).To(ContainSubstring("--configfile=/dev/null"))
		Expect(btcdContainer.ReadinessProbe.Exec.Command[2]).To(ContainSubstring("--rpcserver=\"$RPCSERVER\""))
		Expect(btcdLifecycle.PreStop.Exec.Command[2]).To(ContainSubstring("--configfile=/dev/null"))
		Expect(btcdLifecycle.PreStop.Exec.Command[2]).To(ContainSubstring("--rpcserver=\"$RPCSERVER\""))
		Expect(btcdContainer.Env).To(ContainElement(corev1.EnvVar{
			Name:  "RPCSERVER",
			Value: bitcoinNodeOwnedResourceName(BitcoinNodeName) + "." + Namespace + ".svc.cluster.local:18556",
		}))

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

	It("blocks mainnet unless it is explicitly enabled", func() {
		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{Name: BitcoinNodeName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.BitcoinNodeSpec{
				Network: "mainnet",
			},
		}
		Expect(k8sClient.Create(ctx, bitcoinNode)).To(Succeed())

		reconciler := BitcoinNodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: bitcoinNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		found := &bitcoinv1alpha1.BitcoinNode{}
		Expect(k8sClient.Get(ctx, bitcoinNodeNamespaceName, found)).To(Succeed())
		Expect(found.Status.Network).To(Equal("mainnet"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NetworkReady").Status).To(Equal(metav1.ConditionFalse))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "NetworkReady").Reason).To(Equal("MainnetOptInRequired"))
		Expect(meta.FindStatusCondition(found.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionFalse))

		statefulSet := &appsv1.StatefulSet{}
		err = k8sClient.Get(ctx, statefulSetNamespaceName, statefulSet)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("starts mainnet only after explicit opt-in and does not use a test-network flag", func() {
		bitcoinNode := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{Name: BitcoinNodeName, Namespace: Namespace},
			Spec: bitcoinv1alpha1.BitcoinNodeSpec{
				Network: "mainnet",
				Safety: bitcoinv1alpha1.NetworkSafetyPolicy{AllowMainnet: true},
				RPCServer: bitcoinv1alpha1.RPCServer{
					CertSecret:           "btcd-rpc-tls",
					ApiAuthSecretName:    "btcd-rpc-creds",
					ApiUserSecretKey:     "username",
					ApiPasswordSecretKey: "password",
				},
			},
		}
		Expect(k8sClient.Create(ctx, bitcoinNode)).To(Succeed())

		reconciler := BitcoinNodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: bitcoinNodeNamespaceName})
		Expect(err).ToNot(HaveOccurred())

		statefulSet := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, statefulSetNamespaceName, statefulSet)).To(Succeed())
		args := statefulSet.Spec.Template.Spec.Containers[0].Args
		Expect(args).ToNot(ContainElement("--simnet"))
		Expect(args).ToNot(ContainElement("--testnet"))
		Expect(args).ToNot(ContainElement("--regtest"))
		Expect(args).ToNot(ContainElement("--signet"))
	})

})
