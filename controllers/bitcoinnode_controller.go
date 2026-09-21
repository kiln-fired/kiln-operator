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
	"fmt"
	"github.com/btcsuite/btcd/rpcclient"
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
	"time"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

// BitcoinNodeReconciler reconciles a BitcoinNode object
const bitcoinNodeFinalizer = "bitcoin.kiln-fired.github.io/stateful-cleanup"

type BitcoinNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services;secrets,verbs=get;list;watch;create;update;patch;delete

func resolvedBitcoinNetwork(b *bitcoinv1alpha1.BitcoinNode) string {
	if b.Spec.Network == "" {
		return "simnet"
	}
	return b.Spec.Network
}

func bitcoinNetworkFlag(network string) (string, error) {
	switch network {
	case "simnet":
		return "--simnet", nil
	case "testnet":
		return "--testnet", nil
	case "regtest":
		return "--regtest", nil
	case "signet":
		return "--signet", nil
	case "mainnet":
		return "", nil
	default:
		return "", fmt.Errorf("unsupported Bitcoin network %q", network)
	}
}

func validateBitcoinNetworkPolicy(b *bitcoinv1alpha1.BitcoinNode) (string, string, error) {
	network := resolvedBitcoinNetwork(b)
	if _, err := bitcoinNetworkFlag(network); err != nil {
		return network, "UnsupportedNetwork", err
	}
	if network == "mainnet" {
		if !b.Spec.Safety.AllowMainnet {
			return network, "MainnetOptInRequired", fmt.Errorf("mainnet requires spec.safety.allowMainnet=true")
		}
		if b.Spec.Mining.CpuMiningEnabled || b.Spec.Mining.MinBlocks > 0 || b.Spec.Mining.PeriodicBlocksEnabled {
			return network, "MainnetMiningForbidden", fmt.Errorf("Kiln automated mining controls are not allowed on mainnet")
		}
	}
	return network, "PolicyAccepted", nil
}

func (r *BitcoinNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	bitcoinNode := &bitcoinv1alpha1.BitcoinNode{}
	err := r.Get(ctx, req.NamespacedName, bitcoinNode)

	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Bitcoin resource not found.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get BitcoinNode")
		return ctrl.Result{}, err
	}

	if !bitcoinNode.DeletionTimestamp.IsZero() {
		return r.finalizeBitcoinNode(ctx, bitcoinNode)
	}

	if !containsString(bitcoinNode.Finalizers, bitcoinNodeFinalizer) {
		bitcoinNode.Finalizers = append(bitcoinNode.Finalizers, bitcoinNodeFinalizer)
		if err := r.Update(ctx, bitcoinNode); err != nil {
			return ctrl.Result{}, err
		}
	}

	network, networkReason, networkErr := validateBitcoinNetworkPolicy(bitcoinNode)
	bitcoinNode.Status.Network = network
	if networkErr != nil {
		meta.SetStatusCondition(&bitcoinNode.Status.Conditions, metav1.Condition{
			Type:               "NetworkReady",
			Status:             metav1.ConditionFalse,
			Reason:             networkReason,
			Message:            networkErr.Error(),
			ObservedGeneration: bitcoinNode.Generation,
		})
		meta.SetStatusCondition(&bitcoinNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             networkReason,
			Message:            networkErr.Error(),
			ObservedGeneration: bitcoinNode.Generation,
		})
		if err := r.Status().Update(ctx, bitcoinNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	meta.SetStatusCondition(&bitcoinNode.Status.Conditions, metav1.Condition{
		Type:               "NetworkReady",
		Status:             metav1.ConditionTrue,
		Reason:             networkReason,
		Message:            "Bitcoin network policy accepted for " + network,
		ObservedGeneration: bitcoinNode.Generation,
	})

	meta.SetStatusCondition(&bitcoinNode.Status.Conditions, metav1.Condition{
		Type:               "StorageFenced",
		Status:             metav1.ConditionTrue,
		Reason:             "ReadWriteOncePod",
		Message:            "Bitcoin data volume is restricted to a single pod",
		ObservedGeneration: bitcoinNode.Generation,
	})

	//Reconcile StatefulSet
	foundStatefulSet := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: bitcoinNode.Name, Namespace: bitcoinNode.Namespace}, foundStatefulSet)

	if err != nil && errors.IsNotFound(err) {
		ss := r.statefulsetForBitcoinNode(bitcoinNode)
		log.Info("Creating a new StatefulSet", "StatefulSet.Namespace", ss.Namespace, "StatefulSet.Name", ss.Name)
		err = r.Create(ctx, ss)
		if err != nil {
			log.Error(err, "Failed to create new StatefulSet", "StatefulSet.Namespace", ss.Namespace, "StatefulSet.Name", ss.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get StatefulSet")
		return ctrl.Result{}, err
	}

	// Reconcile Service
	foundService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: bitcoinNode.Name, Namespace: bitcoinNode.Namespace}, foundService)

	if err != nil && errors.IsNotFound(err) {
		svc := r.serviceForBitcoinNode(bitcoinNode)
		log.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		err = r.Create(ctx, svc)
		if err != nil {
			log.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	foundCertSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: bitcoinNode.Spec.RPCServer.CertSecret, Namespace: bitcoinNode.Namespace}, foundCertSecret)

	if err != nil {
		log.Error(err, "Failed to get Secret")
		return ctrl.Result{}, err
	}

	caCert := foundCertSecret.Data["ca.crt"]

	foundCredSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: bitcoinNode.Spec.RPCServer.ApiAuthSecretName, Namespace: bitcoinNode.Namespace}, foundCredSecret)

	if err != nil {
		log.Error(err, "Failed to get Secret")
		return ctrl.Result{}, err
	}

	rpcUser := string(foundCredSecret.Data[bitcoinNode.Spec.RPCServer.ApiUserSecretKey])
	rpcPass := string(foundCredSecret.Data[bitcoinNode.Spec.RPCServer.ApiPasswordSecretKey])

	connCfg := &rpcclient.ConnConfig{
		Host:         bitcoinNode.Name + "." + bitcoinNode.Namespace + "." + "svc.cluster.local:18556",
		User:         rpcUser,
		Pass:         rpcPass,
		Certificates: caCert,
		HTTPPostMode: true,
	}

	btcdClient, err := rpcclient.New(connCfg, nil)
	blockCount, err := btcdClient.GetBlockCount()

	if err != nil {
		log.Error(err, "Failed to get the block count")
		meta.SetStatusCondition(&bitcoinNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "RPCUnavailable",
			Message:            "Bitcoin RPC is not yet available",
			ObservedGeneration: bitcoinNode.Generation,
		})
		_ = r.Status().Update(ctx, bitcoinNode)
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	log.Info("Retreived block count", "count", blockCount)

	peer := bitcoinNode.Spec.Peer

	if peer != "" {
		perm := "perm"
		err := btcdClient.Node("connect", peer, &perm)
		if err != nil {
			log.Info("Failed to add peer", "error", err.Error())
			return ctrl.Result{}, err
		}
		log.Info("Connected to peer", "peer", peer)
	}

	minBlocks := bitcoinNode.Spec.Mining.MinBlocks

	if minBlocks != 0 && blockCount < minBlocks {
		numBlocksToGenerate := minBlocks - blockCount
		hashes, err := btcdClient.Generate(uint32(numBlocksToGenerate))
		if err != nil {
			log.Info("Failed to generate blocks", "error", err.Error())
			return ctrl.Result{Requeue: true}, nil
		}
		log.Info("Generated blocks", "numBlocks", len(hashes))
	}

	miningEnabled, err := btcdClient.GetGenerate()

	if err != nil {
		log.Info("Failed to determine if mining is enabled")
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	log.Info("Got a mining status", "miningEnabled", miningEnabled)
	enableMining := bitcoinNode.Spec.Mining.CpuMiningEnabled

	if enableMining && !miningEnabled {
		err = btcdClient.SetGenerate(true, 1)
		if err != nil {
			log.Info("Failed to enable mining", "error", err.Error())
		}
		log.Info("Enabled mining")
	}

	bitcoinNode.Status.LastBlockCount = blockCount
	meta.SetStatusCondition(&bitcoinNode.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "RPCReady",
		Message:            "Bitcoin RPC is available",
		ObservedGeneration: bitcoinNode.Generation,
	})

	err = r.Status().Update(ctx, bitcoinNode)
	if err != nil {
		log.Error(err, "Failed to update BitcoinNode status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *BitcoinNodeReconciler) statefulsetForBitcoinNode(b *bitcoinv1alpha1.BitcoinNode) *appsv1.StatefulSet {
	ls := labelsForBitcoinNode(b.Name)
	size := int32(1)

	btcdImage := b.Spec.ContainerImages.BtcdImage
	if btcdImage == "" {
		btcdImage = "ghcr.io/btcsuite/btcd:v0.26.2"
	}
	timerImage := b.Spec.ContainerImages.TimerImage
	if timerImage == "" {
		timerImage = "ghcr.io/btcsuite/btcd:v0.26.2"
	}
	rewardAddressKey := b.Spec.Mining.RewardAddress.SecretKey
	if rewardAddressKey == "" {
		rewardAddressKey = "np2wkhAddress"
	}
	network := resolvedBitcoinNetwork(b)
	networkFlag, _ := bitcoinNetworkFlag(network)

	environment := []corev1.EnvVar{
		{
			Name:  "HOME",
			Value: "/home/btcd",
		},
		{
			Name:  "NETWORKFLAG",
			Value: networkFlag,
		},
		{
			Name: "RPCUSER",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: b.Spec.RPCServer.ApiAuthSecretName,
					},
					Key: b.Spec.RPCServer.ApiUserSecretKey,
				},
			},
		},
		{
			Name: "RPCPASS",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: b.Spec.RPCServer.ApiAuthSecretName,
					},
					Key: b.Spec.RPCServer.ApiPasswordSecretKey,
				},
			},
		},
	}

	if b.Spec.Mining.RewardAddress.SecretName != "" {
		rewardAddress := corev1.EnvVar{
			Name: "MINING_ADDRESS",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: b.Spec.Mining.RewardAddress.SecretName,
					},
					Key: rewardAddressKey,
				},
			},
		}
		environment = append(environment, rewardAddress)
	}

	btcdArgs := []string{
		"--listen=0.0.0.0:18555",
		"--rpclisten=0.0.0.0:18556",
		"--rpcuser=$(RPCUSER)",
		"--rpcpass=$(RPCPASS)",
		"--rpccert=/rpc/rpc.cert",
		"--rpckey=/rpc/rpc.key",
		"--datadir=/data",
		"--logdir=/data/logs",
	}
	if networkFlag != "" {
		btcdArgs = append([]string{networkFlag}, btcdArgs...)
	}
	if b.Spec.Mining.RewardAddress.SecretName != "" {
		btcdArgs = append(btcdArgs, "--miningaddr=$(MINING_ADDRESS)")
	}

	btcd := corev1.Container{
		Image:   btcdImage,
		Name:    "btcd",
		Command: []string{"btcd"},
		Args:    btcdArgs,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: 18555,
				Name:          "server",
			},
			{
				ContainerPort: 18556,
				Name:          "rpc",
			},
		},
		Env: environment,
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
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/sh",
						"-c",
						"btcctl --configfile=/dev/null $NETWORKFLAG --rpcserver=127.0.0.1:18556 --rpcuser=\"$RPCUSER\" --rpcpass=\"$RPCPASS\" --rpccert=/rpc/rpc.cert stop || true",
					},
				},
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/sh",
						"-c",
						"btcctl --configfile=/dev/null $NETWORKFLAG --rpcserver=127.0.0.1:18556 --rpcuser=\"$RPCUSER\" --rpcpass=\"$RPCPASS\" --rpccert=/rpc/rpc.cert getblockcount",
					},
				},
			},
			InitialDelaySeconds: 5,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/sh",
						"-c",
						"btcctl --configfile=/dev/null $NETWORKFLAG --rpcserver=127.0.0.1:18556 --rpcuser=\"$RPCUSER\" --rpcpass=\"$RPCPASS\" --rpccert=/rpc/rpc.cert getblockcount",
					},
				},
			},
			InitialDelaySeconds: 5,
		},
		Resources: b.Spec.Resources,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "btcd-home",
				MountPath: "/home/btcd",
			},
			{
				Name:      "btcd-data",
				MountPath: "/data",
			},
			{
				Name:      "rpc-cert",
				MountPath: "/rpc/rpc.cert",
				SubPath:   "tls.crt",
			},
			{
				Name:      "rpc-cert",
				MountPath: "/rpc/rpc.key",
				SubPath:   "tls.key",
			},
		},
	}

	timer := corev1.Container{
		Image:   timerImage,
		Name:    "timer",
		Command: []string{"/bin/sh"},
		Args: []string{"-c", fmt.Sprintf(
			"while true; do btcctl --configfile=/dev/null $NETWORKFLAG --rpcserver=127.0.0.1:18556 --rpcuser=$RPCUSER --rpcpass=$RPCPASS --rpccert=/rpc/rpc.cert generate 1; sleep %d; done",
			b.Spec.Mining.SecondsPerBlock,
		)},
		Env:     environment,
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
			{
				Name:      "btcd-home",
				MountPath: "/home/btcd",
			},
			{
				Name:      "btcd-data",
				MountPath: "/data",
			},
			{
				Name:      "rpc-cert",
				MountPath: "/rpc/rpc.cert",
				SubPath:   "tls.crt",
			},
			{
				Name:      "rpc-cert",
				MountPath: "/rpc/rpc.key",
				SubPath:   "tls.key",
			},
		},
	}

	containers := []corev1.Container{btcd}

	if b.Spec.Mining.PeriodicBlocksEnabled == true {
		containers = append(containers, timer)
	}

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.Name,
			Namespace: b.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &size,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			ServiceName: b.Name,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To(int64(60)),
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: ptr.To(int64(65532)),
					},
					Containers: containers,
					Volumes: []corev1.Volume{
						{
							Name: "btcd-home",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "rpc-cert",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: b.Spec.RPCServer.CertSecret,
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
					Name:   "btcd-data",
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("2Gi"),
						},
					},
				},
			}},
		},
	}

	err := ctrl.SetControllerReference(b, ss, r.Scheme)
	if err != nil {
		return nil
	}
	return ss
}

func (r *BitcoinNodeReconciler) serviceForBitcoinNode(b *bitcoinv1alpha1.BitcoinNode) *corev1.Service {
	ls := labelsForBitcoinNode(b.Name)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    ls,
			Name:      b.Name,
			Namespace: b.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type: "ClusterIP",
			Ports: []corev1.ServicePort{
				{
					Name:       "server",
					Protocol:   "TCP",
					Port:       18555,
					TargetPort: intstr.FromInt(18555),
				},
				{
					Name:       "rpc",
					Protocol:   "TCP",
					Port:       18556,
					TargetPort: intstr.FromInt(18556),
				},
			},
			Selector:                 ls,
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
		},
	}

	err := ctrl.SetControllerReference(b, svc, r.Scheme)
	if err != nil {
		return nil
	}
	return svc
}

func (r *BitcoinNodeReconciler) finalizeBitcoinNode(ctx context.Context, b *bitcoinv1alpha1.BitcoinNode) (ctrl.Result, error) {
	if !containsString(b.Finalizers, bitcoinNodeFinalizer) {
		return ctrl.Result{}, nil
	}

	ss := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: b.Name, Namespace: b.Namespace}, ss)
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
		return ctrl.Result{RequeueAfter: time.Second * 2}, nil
	}

	b.Finalizers = removeString(b.Finalizers, bitcoinNodeFinalizer)
	if err := r.Update(ctx, b); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func removeString(values []string, value string) []string {
	result := values[:0]
	for _, item := range values {
		if item != value {
			result = append(result, item)
		}
	}
	return result
}

func labelsForBitcoinNode(name string) map[string]string {
	return map[string]string{"app": "bitcoinnode", "bitcoinnode_cr": name}
}

// SetupWithManager sets up the controller with the Manager.
func (r *BitcoinNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.BitcoinNode{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
