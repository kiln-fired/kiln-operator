package controllers

import (
	"context"
	"crypto/sha256"
	"net"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

const kubernetesServiceNameMaxLength = 63

func bitcoinNodeOwnedResourceName(name string) string {
	return typedOwnedResourceName(name, "bitcoin")
}

func lightningNodeOwnedResourceName(name string) string {
	return typedOwnedResourceName(name, "lightning")
}

func typedOwnedResourceName(name, ownerType string) string {
	suffix := "-" + ownerType
	if len(name)+len(suffix) <= kubernetesServiceNameMaxLength {
		return name + suffix
	}

	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:4])
	maxBase := kubernetesServiceNameMaxLength - len(suffix) - len(hash) - 1
	return name[:maxBase] + "-" + hash + suffix
}

func controlledBy(obj metav1.Object, owner client.Object, kind string) error {
	ref := metav1.GetControllerOf(obj)
	if ref == nil {
		return fmt.Errorf("%s %s/%s exists but is not controlled by %s %s", kind, obj.GetNamespace(), obj.GetName(), owner.GetObjectKind().GroupVersionKind().Kind, owner.GetName())
	}
	if ref.UID != owner.GetUID() {
		return fmt.Errorf("%s %s/%s is controlled by a different resource", kind, obj.GetNamespace(), obj.GetName())
	}
	return nil
}

func legacyPVCMatches(pvc *corev1.PersistentVolumeClaim, app, labelKey, resourceName string) bool {
	return pvc.Labels["app"] == app && pvc.Labels[labelKey] == resourceName
}

func (r *BitcoinNodeReconciler) ownedResourceName(ctx context.Context, b *bitcoinv1alpha1.BitcoinNode) (string, error) {
	return resolveBitcoinNodeOwnedResourceName(ctx, r.Client, b)
}

func resolveBitcoinNodeOwnedResourceName(ctx context.Context, k8sClient client.Client, b *bitcoinv1alpha1.BitcoinNode) (string, error) {
	typed := bitcoinNodeOwnedResourceName(b.Name)

	current := &appsv1.StatefulSet{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: typed, Namespace: b.Namespace}, current)
	if err == nil {
		if err := controlledBy(current, b, "StatefulSet"); err != nil {
			return "", err
		}
		return typed, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}

	legacy := &appsv1.StatefulSet{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: b.Name, Namespace: b.Namespace}, legacy)
	if err == nil {
		if err := controlledBy(legacy, b, "StatefulSet"); err != nil {
			return "", err
		}
		return b.Name, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}

	pvc := &corev1.PersistentVolumeClaim{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: "btcd-data-" + b.Name + "-0", Namespace: b.Namespace}, pvc)
	if err == nil && legacyPVCMatches(pvc, "bitcoinnode", "bitcoinnode_cr", b.Name) {
		return b.Name, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}
	return typed, nil
}

func (r *LightningNodeReconciler) ownedResourceName(ctx context.Context, l *bitcoinv1alpha1.LightningNode) (string, error) {
	typed := lightningNodeOwnedResourceName(l.Name)

	current := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: typed, Namespace: l.Namespace}, current)
	if err == nil {
		if err := controlledBy(current, l, "StatefulSet"); err != nil {
			return "", err
		}
		return typed, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}

	legacy := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, legacy)
	if err == nil {
		if err := controlledBy(legacy, l, "StatefulSet"); err != nil {
			return "", err
		}
		return l.Name, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}

	pvc := &corev1.PersistentVolumeClaim{}
	err = r.Get(ctx, types.NamespacedName{Name: "lnd-data-" + l.Name + "-0", Namespace: l.Namespace}, pvc)
	if err == nil && legacyPVCMatches(pvc, "lightningnode", "lightningnode_cr", l.Name) {
		return l.Name, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}
	return typed, nil
}


func lightningNodeServiceHost(l *bitcoinv1alpha1.LightningNode) string {
	if l.Status.RPCAddress != "" {
		if host, _, err := net.SplitHostPort(l.Status.RPCAddress); err == nil {
			return host
		}
	}
	return lightningNodeOwnedResourceName(l.Name) + "." + l.Namespace + ".svc.cluster.local"
}
