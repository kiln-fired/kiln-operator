package controllers

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

var _ = Describe("owned resource naming", func() {
	It("uses distinct type-qualified names", func() {
		Expect(bitcoinNodeOwnedResourceName("shared")).To(Equal("shared-bitcoin"))
		Expect(lightningNodeOwnedResourceName("shared")).To(Equal("shared-lightning"))
		Expect(bitcoinNodeOwnedResourceName("shared")).ToNot(Equal(lightningNodeOwnedResourceName("shared")))
	})

	It("keeps generated Service names within the Kubernetes length limit", func() {
		name := strings.Repeat("a", 100)
		bitcoinName := bitcoinNodeOwnedResourceName(name)
		lightningName := lightningNodeOwnedResourceName(name)

		Expect(bitcoinName).To(HaveLen(kubernetesServiceNameMaxLength))
		Expect(lightningName).To(HaveLen(kubernetesServiceNameMaxLength))
		Expect(bitcoinName).To(HaveSuffix("-bitcoin"))
		Expect(lightningName).To(HaveSuffix("-lightning"))
		Expect(bitcoinName).To(Equal(bitcoinNodeOwnedResourceName(name)))
		Expect(lightningName).To(Equal(lightningNodeOwnedResourceName(name)))
	})

	It("rejects a child resource controlled by a different owner", func() {
		node := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "test", UID: types.UID("expected-uid")},
		}
		controller := true
		statefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bitcoinNodeOwnedResourceName("shared"),
				Namespace: "test",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "bitcoin.kiln-fired.github.io/v1alpha1",
					Kind:       "LightningNode",
					Name:       "shared",
					UID:        types.UID("different-uid"),
					Controller: &controller,
				}},
			},
		}

		Expect(controlledBy(statefulSet, node, "StatefulSet")).To(MatchError(ContainSubstring("controlled by a different resource")))
	})

	It("preserves legacy Bitcoin naming when a retained legacy PVC exists", func() {
		node := &bitcoinv1alpha1.BitcoinNode{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "test", UID: types.UID("bitcoin-uid")},
		}
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "btcd-data-legacy-0",
				Namespace: "test",
				Labels:    labelsForBitcoinNode("legacy"),
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(node, pvc).Build()
		reconciler := BitcoinNodeReconciler{Client: fakeClient, Scheme: k8sClient.Scheme()}

		name, err := reconciler.ownedResourceName(context.Background(), node)
		Expect(err).ToNot(HaveOccurred())
		Expect(name).To(Equal("legacy"))
	})

	It("preserves legacy Lightning naming when a retained legacy PVC exists", func() {
		node := &bitcoinv1alpha1.LightningNode{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "test", UID: types.UID("lightning-uid")},
		}
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "lnd-data-legacy-0",
				Namespace: "test",
				Labels:    labelsForLightningNode("legacy"),
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(node, pvc).Build()
		reconciler := LightningNodeReconciler{Client: fakeClient, Scheme: k8sClient.Scheme()}

		name, err := reconciler.ownedResourceName(context.Background(), node)
		Expect(err).ToNot(HaveOccurred())
		Expect(name).To(Equal("legacy"))
	})
})
