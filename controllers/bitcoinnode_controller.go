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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"time"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

// BitcoinNodeReconciler reconciles a BitcoinNode object
type BitcoinNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services;secrets,verbs=get;list;watch;create;update;patch;delete

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

	btcd := corev1.Container{
		Image:   b.Spec.ContainerImages.BtcdImage,
		Name:    "btcd",
		Command: []string{"./start-btcd.sh"},
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
		Env: []corev1.EnvVar{
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
			{
				Name: "MINING_ADDRESS",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: b.Spec.Mining.RewardAddress.SecretName,
						},
						Key: b.Spec.Mining.RewardAddress.SecretKey,
					},
				},
			},
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			Privileged:               pointer.Bool(false),
			RunAsNonRoot:             pointer.Bool(true),
			AllowPrivilegeEscalation: pointer.Bool(false),
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/bash",
						"-c",
						"touch .btcd/btcd.conf && ./start-btcctl.sh getinfo",
					},
				},
			},
			InitialDelaySeconds: 5,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/bash",
						"-c",
						"touch .btcd/btcd.conf && ./start-btcctl.sh getinfo",
					},
				},
			},
			InitialDelaySeconds: 5,
		},
		Resources: b.Spec.Resources,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "btcd-home",
				MountPath: ".btcd",
			},
			{
				Name:      "btcd-data",
				MountPath: "data",
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
		Image:   b.Spec.ContainerImages.TimerImage,
		Name:    "timer",
		Command: []string{"/bin/sh"},
		Args:    []string{"-c", fmt.Sprintf("while true; do ./start-btcctl.sh generate 1; sleep %d;done", b.Spec.Mining.SecondsPerBlock)},
		Env: []corev1.EnvVar{
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
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			Privileged:               pointer.Bool(false),
			RunAsNonRoot:             pointer.Bool(true),
			AllowPrivilegeEscalation: pointer.Bool(false),
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "btcd-home",
				MountPath: ".btcd",
			},
			{
				Name:      "btcd-data",
				MountPath: "data",
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
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
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
					AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
					Resources: corev1.ResourceRequirements{
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
