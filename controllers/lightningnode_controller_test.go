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

var _ = Describe("LightningNode controller", func() {

	const Namespace = "test-namespace"
	const LightningNodeName = "test"

	ctx := context.Background()
	lightningNodeNamespaceName := types.NamespacedName{Namespace: Namespace, Name: LightningNodeName}
	statefulSetNamespaceName := types.NamespacedName{Namespace: Namespace, Name: LightningNodeName}

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
		By("cleaning up LightningNode")
		lightningNode := &bitcoinv1alpha1.LightningNode{}
		err := k8sClient.Get(ctx, lightningNodeNamespaceName, lightningNode)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, lightningNode)
		Expect(err).To(Not(HaveOccurred()))

		By("cleaning up StatefulSet")
		statefulSet := &appsv1.StatefulSet{}
		err = k8sClient.Get(ctx, statefulSetNamespaceName, statefulSet)
		Expect(err).To(Not(HaveOccurred()))
		err = k8sClient.Delete(ctx, statefulSet)
		Expect(err).To(Not(HaveOccurred()))
	})

	It("should reconcile the LightningNode instance", func() {

		lightningNode := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{
				Name:      LightningNodeName,
				Namespace: Namespace,
			},
			Spec: bitcoinv1alpha1.LightningNodeSpec{
				BitcoinConnection: bitcoinv1alpha1.BitcoinConnection{
					Host:       "btcd",
					Network:    "simnet",
					CertSecret: "btcd-rpc-tls",
					User:       "node-user",
					Password:   "st4cks4ts",
				},
				Wallet: bitcoinv1alpha1.Wallet{
					Password: bitcoinv1alpha1.WalletPassword{
						SecretName: "alice-wallet",
						SecretKey:  "password",
					},
					Seed: bitcoinv1alpha1.SeedImport{
						SecretName: "mining-wallet",
					},
				},
			},
		}

		By("creating the custom resource for the kind LightningNode")
		err := k8sClient.Create(ctx, lightningNode)
		Expect(err).To(Not(HaveOccurred()))

		By("checking if the custom resource was successfully created")
		Eventually(func() error {
			foundLightningNode := &bitcoinv1alpha1.LightningNode{}
			return k8sClient.Get(ctx, lightningNodeNamespaceName, foundLightningNode)
		}, time.Minute, time.Second).Should(Succeed())

		By("reconciling the custom resource created")
		lightningNodeReconciler := LightningNodeReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		_, err = lightningNodeReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: lightningNodeNamespaceName,
		})
		Expect(err).To(Not(HaveOccurred()))

		By("checking if a statefulset was successfully created in the reconciliation")
		foundStatefulSet := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, statefulSetNamespaceName, foundStatefulSet)
		}, time.Minute, time.Second).Should(Succeed())

		By("checking if the wallet password is referenced and mounted")
		Eventually(func() error {
			volumeExists := false
			mainVolumeMountExists := false
			initVolumeMountExists := false

			for _, volume := range foundStatefulSet.Spec.Template.Spec.Volumes {
				if volume.Name == "wallet-password" {
					volumeExists = true
					Expect(volume.VolumeSource.Secret.SecretName).To(Equal(lightningNode.Spec.Wallet.Password.SecretName))
				}
			}
			for _, container := range foundStatefulSet.Spec.Template.Spec.Containers {
				if container.Name == "lnd" {
					for _, volumeMount := range container.VolumeMounts {
						if volumeMount.Name == "wallet-password" {
							mainVolumeMountExists = true
							Expect(volumeMount.MountPath).To(Equal("/secret/wallet-password"))
							Expect(volumeMount.SubPath).To(Equal(lightningNode.Spec.Wallet.Password.SecretKey))
						}
					}
				}
			}
			for _, container := range foundStatefulSet.Spec.Template.Spec.InitContainers {
				if container.Name == "lnd-init" {
					for _, volumeMount := range container.VolumeMounts {
						if volumeMount.Name == "wallet-password" {
							initVolumeMountExists = true
							Expect(volumeMount.MountPath).To(Equal("/secret/wallet-password"))
							Expect(volumeMount.SubPath).To(Equal(lightningNode.Spec.Wallet.Password.SecretKey))
						}
					}
				}
			}
			Expect(volumeExists).To(BeTrue())
			Expect(initVolumeMountExists).To(BeTrue())
			Expect(mainVolumeMountExists).To(BeTrue())
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("checking for the seed volume and volume mount")
		Eventually(func() error {
			volumeExists := false
			volumeMountExists := false
			for _, volume := range foundStatefulSet.Spec.Template.Spec.Volumes {
				if volume.Name == "seed" {
					volumeExists = true
					Expect(volume.VolumeSource.Secret.SecretName).To(Equal(lightningNode.Spec.Wallet.Seed.SecretName))
				}
			}
			for _, container := range foundStatefulSet.Spec.Template.Spec.InitContainers {
				if container.Name == "lnd-init" {
					for _, volumeMount := range container.VolumeMounts {
						if volumeMount.Name == "seed" {
							volumeMountExists = true
							Expect(volumeMount.MountPath).To(Equal("/secret/seed"))
							Expect(volumeMount.SubPath).To(Equal("seed"))
						}
					}
				}
			}
			Expect(volumeExists).To(BeTrue())
			Expect(volumeMountExists).To(BeTrue())
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("checking if the container arguments are configured with a wallet password")
		Eventually(func() error {
			Expect(foundStatefulSet.Spec.Template.Spec.InitContainers).To(Not(BeEmpty()))
			for _, container := range foundStatefulSet.Spec.Template.Spec.InitContainers {
				if container.Name == "lnd-init" {
					Expect(container.Args[0]).To(Equal("init-wallet"))
					Expect(container.Args[3]).To(ContainSubstring("/secret/seed"))
					Expect(container.Args[4]).To(ContainSubstring("/secret/wallet-password"))
				}
			}
			for _, container := range foundStatefulSet.Spec.Template.Spec.Containers {
				if container.Name == "lnd" {
					Expect(container.Args[0]).To(ContainSubstring("/secret/wallet-password"))
				}
			}
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("checking if the pvc is mounted")
		Eventually(func() error {
			volumeClaimTemplateExists := false
			mainVolumeMountExists := false
			initVolumeMountExists := false

			for _, volumeClaimTemplate := range foundStatefulSet.Spec.VolumeClaimTemplates {
				if volumeClaimTemplate.Name == "lnd-home" {
					volumeClaimTemplateExists = true
					Expect(volumeClaimTemplate.ObjectMeta.Name).To(Equal("lnd-home"))
				}
			}
			for _, container := range foundStatefulSet.Spec.Template.Spec.Containers {
				if container.Name == "lnd" {
					for _, volumeMount := range container.VolumeMounts {
						if volumeMount.Name == "lnd-home" {
							mainVolumeMountExists = true
						}
					}
				}
			}
			for _, container := range foundStatefulSet.Spec.Template.Spec.InitContainers {
				if container.Name == "lnd-init" {
					for _, volumeMount := range container.VolumeMounts {
						if volumeMount.Name == "lnd-home" {
							initVolumeMountExists = true
						}
					}
				}
			}
			Expect(volumeClaimTemplateExists).To(BeTrue())
			Expect(initVolumeMountExists).To(BeTrue())
			Expect(mainVolumeMountExists).To(BeTrue())
			return nil
		}, time.Minute, time.Second).Should(Succeed())
	})

})
