/*
Copyright 2021.

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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

// LightningNodeReconciler reconciles a LightningNode object
type LightningNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the LightningNode object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *LightningNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	lightningNode := &bitcoinv1alpha1.LightningNode{}
	err := r.Get(ctx, req.NamespacedName, lightningNode)

	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("LightningNode resource not found.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get LightningNode")
		return ctrl.Result{}, err
	}

	// Reconcile StatefulSet
	foundStatefulSet := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: lightningNode.Name, Namespace: lightningNode.Namespace}, foundStatefulSet)

	if err != nil && errors.IsNotFound(err) {
		ss := r.statefulsetForLightningNode(lightningNode)
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
	err = r.Get(ctx, types.NamespacedName{Name: lightningNode.Name, Namespace: lightningNode.Namespace}, foundService)

	if err != nil && errors.IsNotFound(err) {
		svc := r.serviceForLightningNode(lightningNode)
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

	return ctrl.Result{}, nil
}

func (r *LightningNodeReconciler) statefulsetForLightningNode(l *bitcoinv1alpha1.LightningNode) *appsv1.StatefulSet {
	ls := labelsForLightningNode(l.Name)
	size := int32(1)
	// Bitcoin node hostname is hard-coded in lnd image
	// bitcoinHost := l.Spec.BitcoinConnection.Host
	bitcoinNetwork := l.Spec.BitcoinConnection.Network
	bitcoinCertSecret := l.Spec.BitcoinConnection.CertSecret
	bitcoinUser := l.Spec.BitcoinConnection.User
	bitcoinPass := l.Spec.BitcoinConnection.Password

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      l.Name,
			Namespace: l.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &size,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			ServiceName: l.Name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image:   "quay.io/kiln-fired/lnd:latest",
						Name:    "lnd",
						Command: []string{"./start-lnd.sh"},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 9735,
								Name:          "p2p",
							},
							{
								ContainerPort: 10009,
								Name:          "rpc",
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "NETWORK",
								Value: bitcoinNetwork,
							},
							{
								Name:  "RPCUSER",
								Value: bitcoinUser,
							},
							{
								Name:  "RPCPASS",
								Value: bitcoinPass,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "lnd-home",
								MountPath: ".lnd",
							},
							{
								Name:      "rpc-cert",
								MountPath: "/rpc/rpc.cert",
								SubPath:   "tls.crt",
							},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "rpc-cert",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: bitcoinCertSecret,
							},
						},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
					Name:   "lnd-home",
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName(corev1.ResourceStorage): resource.MustParse("2Gi"),
						},
					},
				},
			}},
		},
	}

	ctrl.SetControllerReference(l, ss, r.Scheme)
	return ss
}

func (r *LightningNodeReconciler) serviceForLightningNode(l *bitcoinv1alpha1.LightningNode) *corev1.Service {
	ls := labelsForLightningNode(l.Name)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    ls,
			Name:      l.Name,
			Namespace: l.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type: "ClusterIP",
			Ports: []corev1.ServicePort{
				{
					Name:       "p2p",
					Protocol:   "TCP",
					Port:       9735,
					TargetPort: intstr.FromInt(int(9735)),
				},
				{
					Name:       "rpc",
					Protocol:   "TCP",
					Port:       10009,
					TargetPort: intstr.FromInt(int(10009)),
				},
			},
			Selector:                 ls,
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
		},
	}

	ctrl.SetControllerReference(l, svc, r.Scheme)
	return svc
}

func labelsForLightningNode(name string) map[string]string {
	return map[string]string{"app": "lightningnode", "lightningnode_cr": name}
}

// SetupWithManager sets up the controller with the Manager.
func (r *LightningNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.LightningNode{}).
		Complete(r)
}
