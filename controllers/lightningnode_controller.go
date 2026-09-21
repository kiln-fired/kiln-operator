/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

const lightningNodeFinalizer = "bitcoin.kiln-fired.github.io/lightning-stateful-cleanup"

// LightningNodeReconciler reconciles a LightningNode object
type LightningNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes/finalizers,verbs=update
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services;secrets,verbs=get;list;watch

func (r *LightningNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	lightningNode := &bitcoinv1alpha1.LightningNode{}
	if err := r.Get(ctx, req.NamespacedName, lightningNode); err != nil {
		if errors.IsNotFound(err) {
			log.Info("LightningNode resource not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !lightningNode.DeletionTimestamp.IsZero() {
		return r.finalizeLightningNode(ctx, lightningNode)
	}

	if !containsString(lightningNode.Finalizers, lightningNodeFinalizer) {
		lightningNode.Finalizers = append(lightningNode.Finalizers, lightningNodeFinalizer)
		if err := r.Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
	}

	meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
		Type:               "StorageFenced",
		Status:             metav1.ConditionTrue,
		Reason:             "ReadWriteOncePod",
		Message:            "Lightning state volume is restricted to a single pod",
		ObservedGeneration: lightningNode.Generation,
	})

	connection, bitcoinReady, err := r.resolveBitcoinConnection(ctx, lightningNode)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !bitcoinReady {
		lightningNode.Status.Phase = "WaitingForBitcoin"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "WalletReady",
			Status:             metav1.ConditionFalse,
			Reason:             "BitcoinUnavailable",
			Message:            "Wallet startup is blocked until the Bitcoin dependency is ready",
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "BitcoinUnavailable",
			Message:            "Lightning node is waiting for its Bitcoin dependency",
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	foundStatefulSet := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: lightningNode.Name, Namespace: lightningNode.Namespace}, foundStatefulSet)
	if err != nil && errors.IsNotFound(err) {
		ss := r.statefulsetForLightningNode(lightningNode, connection)
		log.Info("Creating a new StatefulSet", "StatefulSet.Namespace", ss.Namespace, "StatefulSet.Name", ss.Name)
		if err := r.Create(ctx, ss); err != nil {
			return ctrl.Result{}, err
		}
		lightningNode.Status.Phase = "Initializing"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "WalletReady",
			Status:             metav1.ConditionFalse,
			Reason:             "Initializing",
			Message:            "LND wallet initialization and startup are in progress",
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Starting",
			Message:            "Lightning StatefulSet has been created and is starting",
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	foundService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: lightningNode.Name, Namespace: lightningNode.Namespace}, foundService)
	if err != nil && errors.IsNotFound(err) {
		svc := r.serviceForLightningNode(lightningNode)
		log.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		if err := r.Create(ctx, svc); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	if foundStatefulSet.Status.ReadyReplicas < 1 {
		lightningNode.Status.Phase = "Starting"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "WalletReady",
			Status:             metav1.ConditionFalse,
			Reason:             "Starting",
			Message:            "LND has not yet passed its readiness check",
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "PodNotReady",
			Message:            "Lightning pod is not ready",
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	lightningNode.Status.Phase = "Ready"
	meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
		Type:               "WalletReady",
		Status:             metav1.ConditionTrue,
		Reason:             "RPCReady",
		Message:            "LND wallet is initialized, unlocked, and responding to authenticated RPC",
		ObservedGeneration: lightningNode.Generation,
	})
	meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "RPCReady",
		Message:            "Lightning node is ready",
		ObservedGeneration: lightningNode.Generation,
	})
	if err := r.Status().Update(ctx, lightningNode); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *LightningNodeReconciler) resolveBitcoinConnection(ctx context.Context, l *bitcoinv1alpha1.LightningNode) (bitcoinv1alpha1.BitcoinConnection, bool, error) {
	connection := l.Spec.BitcoinConnection
	if connection.Network == "" {
		connection.Network = "simnet"
	}

	if connection.NodeRef == "" {
		if connection.Host == "" || connection.CertSecret == "" || connection.ApiAuthSecretName == "" ||
			connection.ApiUserSecretKey == "" || connection.ApiPasswordSecretKey == "" {
			meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
				Type:               "BitcoinReady",
				Status:             metav1.ConditionFalse,
				Reason:             "ConnectionIncomplete",
				Message:            "bitcoinConnection must provide nodeRef or complete RPC connection fields",
				ObservedGeneration: l.Generation,
			})
			return connection, false, nil
		}
		meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
			Type:               "BitcoinReady",
			Status:             metav1.ConditionTrue,
			Reason:             "ExternalConfiguration",
			Message:            "Bitcoin RPC connection is configured explicitly",
			ObservedGeneration: l.Generation,
		})
		return connection, true, nil
	}

	bitcoinNode := &bitcoinv1alpha1.BitcoinNode{}
	if err := r.Get(ctx, types.NamespacedName{Name: connection.NodeRef, Namespace: l.Namespace}, bitcoinNode); err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
				Type:               "BitcoinReady",
				Status:             metav1.ConditionFalse,
				Reason:             "BitcoinNodeNotFound",
				Message:            "Referenced BitcoinNode does not exist",
				ObservedGeneration: l.Generation,
			})
			return connection, false, nil
		}
		return connection, false, err
	}

	ready := meta.FindStatusCondition(bitcoinNode.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
			Type:               "BitcoinReady",
			Status:             metav1.ConditionFalse,
			Reason:             "BitcoinNodeNotReady",
			Message:            "Referenced BitcoinNode is not ready",
			ObservedGeneration: l.Generation,
		})
		return connection, false, nil
	}

	connection.Host = bitcoinNode.Name
	connection.CertSecret = bitcoinNode.Spec.RPCServer.CertSecret
	connection.ApiAuthSecretName = bitcoinNode.Spec.RPCServer.ApiAuthSecretName
	connection.ApiUserSecretKey = bitcoinNode.Spec.RPCServer.ApiUserSecretKey
	connection.ApiPasswordSecretKey = bitcoinNode.Spec.RPCServer.ApiPasswordSecretKey
	meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
		Type:               "BitcoinReady",
		Status:             metav1.ConditionTrue,
		Reason:             "BitcoinNodeReady",
		Message:            "Referenced BitcoinNode is ready",
		ObservedGeneration: l.Generation,
	})
	return connection, true, nil
}

func (r *LightningNodeReconciler) statefulsetForLightningNode(l *bitcoinv1alpha1.LightningNode, connection bitcoinv1alpha1.BitcoinConnection) *appsv1.StatefulSet {
	ls := labelsForLightningNode(l.Name)
	size := int32(1)

	lndImage := l.Spec.ContainerImages.LndImage
	if lndImage == "" {
		lndImage = "docker.io/lightninglabs/lnd:v0.21.0-beta"
	}
	lndInitImage := l.Spec.ContainerImages.LndInitImage
	if lndInitImage == "" {
		lndInitImage = "docker.io/lightninglabs/lndinit:v0.1.36-beta-lnd-v0.21.0-beta"
	}
	mnemonicKey := l.Spec.Wallet.Seed.MnemonicKey
	if mnemonicKey == "" {
		mnemonicKey = "mnemonic"
	}
	passphraseKey := l.Spec.Wallet.Seed.PassphraseKey
	if passphraseKey == "" {
		passphraseKey = "passphrase"
	}
	network := connection.Network
	if network == "" {
		network = "simnet"
	}

	lnd := corev1.Container{
		Image:   lndImage,
		Name:    "lnd",
		Command: []string{"lnd"},
		Args: []string{
			"--lnddir=/data",
			"--wallet-unlock-password-file=/secret/wallet-password",
			"--bitcoin.active",
			"--bitcoin.$(NETWORK)",
			"--bitcoin.node=btcd",
			"--btcd.rpccert=/rpc/rpc.cert",
			"--btcd.rpchost=$(RPCHOST)",
			"--btcd.rpcuser=$(RPCUSER)",
			"--btcd.rpcpass=$(RPCPASS)",
			"--rpclisten=0.0.0.0:10009",
			"--listen=0.0.0.0:9735",
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: 9735, Name: "p2p"},
			{ContainerPort: 10009, Name: "rpc"},
		},
		Env: []corev1.EnvVar{
			{Name: "NETWORK", Value: network},
			{Name: "RPCHOST", Value: connection.Host},
			{
				Name: "RPCUSER",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: connection.ApiAuthSecretName},
					Key:                  connection.ApiUserSecretKey,
				}},
			},
			{
				Name: "RPCPASS",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: connection.ApiAuthSecretName},
					Key:                  connection.ApiPasswordSecretKey,
				}},
			},
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			Privileged:               ptr.To(false),
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(int64(65532)),
			RunAsGroup:               ptr.To(int64(65532)),
			AllowPrivilegeEscalation: ptr.To(false),
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Lifecycle: &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{
				"/bin/sh",
				"-c",
				"lncli --lnddir=/data --network=$NETWORK stop || true",
			}}},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
				"/bin/sh",
				"-c",
				"lncli --lnddir=/data --network=$NETWORK getinfo >/dev/null",
			}}},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "lnd-data", MountPath: "/data"},
			{Name: "rpc-cert", MountPath: "/rpc/rpc.cert", SubPath: "tls.crt"},
			{Name: "wallet-password", MountPath: "/secret/wallet-password", SubPath: l.Spec.Wallet.Password.SecretKey},
		},
	}

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: l.Name, Namespace: l.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &size,
			Selector:    &metav1.LabelSelector{MatchLabels: ls},
			ServiceName: l.Name,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: ls},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To(int64(60)),
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: ptr.To(int64(65532)),
					},
					InitContainers: []corev1.Container{{
						Image:   lndInitImage,
						Name:    "lnd-init",
						Command: []string{"lndinit"},
						Args: []string{
							"init-wallet",
							"-v",
							"--secret-source=file",
							"--file.seed=/secret/seed/$(SEEDMNEMONICKEY)",
							"--file.seed-passphrase=/secret/seed/$(SEEDPASSPHRASEKEY)",
							"--file.wallet-password=/secret/wallet-password",
							"--init-file.output-wallet-dir=/data/data/chain/bitcoin/$(NETWORK)",
							"--init-file.validate-password",
						},
						Env: []corev1.EnvVar{
							{Name: "NETWORK", Value: network},
							{Name: "SEEDMNEMONICKEY", Value: mnemonicKey},
							{Name: "SEEDPASSPHRASEKEY", Value: passphraseKey},
						},
						SecurityContext: &corev1.SecurityContext{
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							Privileged:               ptr.To(false),
							RunAsNonRoot:             ptr.To(true),
							RunAsUser:                ptr.To(int64(65532)),
							RunAsGroup:               ptr.To(int64(65532)),
							AllowPrivilegeEscalation: ptr.To(false),
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "lnd-data", MountPath: "/data"},
							{Name: "seed", MountPath: "/secret/seed", ReadOnly: true},
							{Name: "wallet-password", MountPath: "/secret/wallet-password", SubPath: l.Spec.Wallet.Password.SecretKey, ReadOnly: true},
						},
					}},
					Containers: []corev1.Container{lnd},
					Volumes: []corev1.Volume{
						{
							Name: "rpc-cert",
							VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
								SecretName: connection.CertSecret,
							}},
						},
						{
							Name: "seed",
							VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
								SecretName: l.Spec.Wallet.Seed.SecretName,
							}},
						},
						{
							Name: "wallet-password",
							VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
								SecretName: l.Spec.Wallet.Password.SecretName,
							}},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Labels: ls, Name: "lnd-data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("2Gi"),
					}},
				},
			}},
		},
	}

	if err := ctrl.SetControllerReference(l, ss, r.Scheme); err != nil {
		return nil
	}
	return ss
}

func (r *LightningNodeReconciler) serviceForLightningNode(l *bitcoinv1alpha1.LightningNode) *corev1.Service {
	ls := labelsForLightningNode(l.Name)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Labels: ls, Name: l.Name, Namespace: l.Namespace},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{Name: "p2p", Protocol: corev1.ProtocolTCP, Port: 9735, TargetPort: intstr.FromInt(9735)},
				{Name: "rpc", Protocol: corev1.ProtocolTCP, Port: 10009, TargetPort: intstr.FromInt(10009)},
			},
			Selector:                 ls,
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
		},
	}
	if err := ctrl.SetControllerReference(l, svc, r.Scheme); err != nil {
		return nil
	}
	return svc
}

func (r *LightningNodeReconciler) finalizeLightningNode(ctx context.Context, l *bitcoinv1alpha1.LightningNode) (ctrl.Result, error) {
	if !containsString(l.Finalizers, lightningNodeFinalizer) {
		return ctrl.Result{}, nil
	}

	ss := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, ss)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err == nil {
		if ss.DeletionTimestamp.IsZero() {
			propagation := metav1.DeletePropagationForeground
			if err := r.Delete(ctx, ss, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	l.Finalizers = removeString(l.Finalizers, lightningNodeFinalizer)
	if err := r.Update(ctx, l); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func labelsForLightningNode(name string) map[string]string {
	return map[string]string{"app": "lightningnode", "lightningnode_cr": name}
}

// SetupWithManager sets up the controller with the Manager.
func (r *LightningNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.LightningNode{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
