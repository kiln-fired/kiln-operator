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
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

const (
	lightningNodeFinalizer       = "bitcoin.kiln-fired.github.io/lightning-stateful-cleanup"
	lightningNodeBitcoinRefIndex = "spec.bitcoinConnection.nodeRef"
)

// LightningNodeReconciler reconciles a LightningNode object
type LightningNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	GetInfo func(context.Context, *bitcoinv1alpha1.LightningNode, *corev1.Secret) (*bitcoinv1alpha1.LightningRuntimeStatus, error)
}

//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes/finalizers,verbs=update
//+kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=bitcoinnodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

type resolvedBitcoinConnection struct {
	Host                 string
	Network              string
	CertSecret           string
	ApiAuthSecretName    string
	ApiUserSecretKey     string
	ApiPasswordSecretKey string
}

func validateLightningNetworkPolicy(l *bitcoinv1alpha1.LightningNode, network string) (string, error) {
	if _, err := bitcoinNetworkFlag(network); err != nil {
		return "UnsupportedNetwork", err
	}
	if network == "mainnet" && !l.Spec.Safety.AllowMainnet {
		return "MainnetOptInRequired", fmt.Errorf("mainnet requires spec.safety.allowMainnet=true")
	}
	return "PolicyAccepted", nil
}

func (r *LightningNodeReconciler) stopLightningWorkloadForPolicy(ctx context.Context, l *bitcoinv1alpha1.LightningNode) (bool, error) {
	ss := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, ss)
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ss.DeletionTimestamp.IsZero() {
		propagation := metav1.DeletePropagationForeground
		if err := r.Delete(ctx, ss, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !errors.IsNotFound(err) {
			return false, err
		}
	}
	return true, nil
}

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
		reason := "BitcoinUnavailable"
		message := "Lightning node is waiting for its Bitcoin dependency"
		lightningNode.Status.Phase = "WaitingForBitcoin"
		if condition := meta.FindStatusCondition(lightningNode.Status.Conditions, "BitcoinReady"); condition != nil {
			reason = condition.Reason
			message = condition.Message
		}
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "WalletReady",
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "Wallet startup is blocked: " + message,
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if lightningNode.Status.Network != "" && lightningNode.Status.Network != connection.Network {
		lightningNode.Status.Phase = "NetworkBlocked"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "NetworkReady",
			Status:             metav1.ConditionFalse,
			Reason:             "NetworkChangeNotAllowed",
			Message:            fmt.Sprintf("resolved Bitcoin network changed from %s to %s", lightningNode.Status.Network, connection.Network),
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "NetworkChangeNotAllowed",
			Message:            "Lightning wallet network cannot change in place",
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		stopping, err := r.stopLightningWorkloadForPolicy(ctx, lightningNode)
		if err != nil {
			return ctrl.Result{}, err
		}
		if stopping {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, nil
	}

	networkReason, networkErr := validateLightningNetworkPolicy(lightningNode, connection.Network)
	lightningNode.Status.Network = connection.Network
	if networkErr != nil {
		lightningNode.Status.Phase = "NetworkBlocked"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "NetworkReady",
			Status:             metav1.ConditionFalse,
			Reason:             networkReason,
			Message:            networkErr.Error(),
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             networkReason,
			Message:            networkErr.Error(),
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		stopping, err := r.stopLightningWorkloadForPolicy(ctx, lightningNode)
		if err != nil {
			return ctrl.Result{}, err
		}
		if stopping {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, nil
	}
	meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
		Type:               "NetworkReady",
		Status:             metav1.ConditionTrue,
		Reason:             networkReason,
		Message:            "Lightning network policy accepted for " + connection.Network,
		ObservedGeneration: lightningNode.Generation,
	})

	if err := r.ensureRPCPublishingResources(ctx, lightningNode); err != nil {
		return ctrl.Result{}, err
	}
	lightningNode.Status.RPCSecretName = lightningRPCSecretName(lightningNode)
	lightningNode.Status.RPCAddress = lightningNode.Name + "." + lightningNode.Namespace + ".svc.cluster.local:10009"

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

	rpcSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: lightningRPCSecretName(lightningNode), Namespace: lightningNode.Namespace}, rpcSecret); err != nil {
		return ctrl.Result{}, err
	}
	credentialsReady := len(rpcSecret.Data["tls.cert"]) > 0 &&
		len(rpcSecret.Data["readonly.macaroon"]) > 0 &&
		len(rpcSecret.Data["invoice.macaroon"]) > 0
	if !credentialsReady {
		lightningNode.Status.Phase = "PublishingCredentials"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "CredentialsReady",
			Status:             metav1.ConditionFalse,
			Reason:             "CredentialsPending",
			Message:            "Waiting for LND TLS and restricted macaroons to be published",
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "CredentialsPending",
			Message:            "LND is running but client credentials are not yet available",
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	getInfo := r.GetInfo
	if getInfo == nil {
		getInfo = fetchLightningRuntime
	}
	runtimeInfo, err := getInfo(ctx, lightningNode, rpcSecret)
	if err != nil {
		lightningNode.Status.Phase = "Degraded"
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "RuntimeReady",
			Status:             metav1.ConditionFalse,
			Reason:             "GetInfoFailed",
			Message:            err.Error(),
			ObservedGeneration: lightningNode.Generation,
		})
		meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "LNDUnavailable",
			Message:            "Authenticated LND GetInfo failed",
			ObservedGeneration: lightningNode.Generation,
		})
		if err := r.Status().Update(ctx, lightningNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	lightningNode.Status.Runtime = *runtimeInfo
	lightningNode.Status.Phase = "Ready"
	meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
		Type:               "RuntimeReady",
		Status:             metav1.ConditionTrue,
		Reason:             "GetInfoSucceeded",
		Message:            "Authenticated LND GetInfo succeeded",
		ObservedGeneration: lightningNode.Generation,
	})
	meta.SetStatusCondition(&lightningNode.Status.Conditions, metav1.Condition{
		Type:               "CredentialsReady",
		Status:             metav1.ConditionTrue,
		Reason:             "Published",
		Message:            "LND TLS certificate and restricted macaroons are available",
		ObservedGeneration: lightningNode.Generation,
	})
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

func (r *LightningNodeReconciler) resolveBitcoinConnection(ctx context.Context, l *bitcoinv1alpha1.LightningNode) (resolvedBitcoinConnection, bool, error) {
	connection := l.Spec.BitcoinConnection
	if connection.NodeRef != "" {
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
				return resolvedBitcoinConnection{}, false, nil
			}
			return resolvedBitcoinConnection{}, false, err
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
			return resolvedBitcoinConnection{}, false, nil
		}

		resolved := resolvedBitcoinConnection{
			Host:                 bitcoinNode.Name,
			Network:              resolvedBitcoinNetwork(bitcoinNode),
			CertSecret:           bitcoinNode.Spec.RPCServer.CertSecret,
			ApiAuthSecretName:    bitcoinNode.Spec.RPCServer.ApiAuthSecretName,
			ApiUserSecretKey:     bitcoinNode.Spec.RPCServer.ApiUserSecretKey,
			ApiPasswordSecretKey: bitcoinNode.Spec.RPCServer.ApiPasswordSecretKey,
		}
		meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
			Type:               "BitcoinReady",
			Status:             metav1.ConditionTrue,
			Reason:             "BitcoinNodeReady",
			Message:            "Referenced BitcoinNode is ready",
			ObservedGeneration: l.Generation,
		})
		return resolved, true, nil
	}

	if connection.External == nil {
		meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
			Type:               "BitcoinReady",
			Status:             metav1.ConditionFalse,
			Reason:             "ConnectionIncomplete",
			Message:            "bitcoinConnection must configure exactly one of nodeRef or external",
			ObservedGeneration: l.Generation,
		})
		return resolvedBitcoinConnection{}, false, nil
	}

	external := connection.External
	resolved := resolvedBitcoinConnection{
		Host:                 external.Host,
		Network:              external.Network,
		CertSecret:           external.CertSecret,
		ApiAuthSecretName:    external.ApiAuthSecretName,
		ApiUserSecretKey:     external.ApiUserSecretKey,
		ApiPasswordSecretKey: external.ApiPasswordSecretKey,
	}
	meta.SetStatusCondition(&l.Status.Conditions, metav1.Condition{
		Type:               "BitcoinReady",
		Status:             metav1.ConditionTrue,
		Reason:             "ExternalConfiguration",
		Message:            "Bitcoin RPC connection is configured explicitly",
		ObservedGeneration: l.Generation,
	})
	return resolved, true, nil
}

func fetchLightningRuntime(ctx context.Context, l *bitcoinv1alpha1.LightningNode, secret *corev1.Secret) (*bitcoinv1alpha1.LightningRuntimeStatus, error) {
	certPEM := secret.Data["tls.cert"]
	macaroon := secret.Data["readonly.macaroon"]
	if len(certPEM) == 0 || len(macaroon) == 0 {
		return nil, fmt.Errorf("published LND runtime credentials are incomplete")
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		return nil, fmt.Errorf("unable to parse published LND TLS certificate")
	}

	host := l.Name + "." + l.Namespace + ".svc.cluster.local"
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+":8080/v1/getinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Grpc-Metadata-macaroon", hex.EncodeToString(macaroon))
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LND GetInfo request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("LND GetInfo returned %s: %s", resp.Status, string(body))
	}

	var info struct {
		Version             string `json:"version"`
		IdentityPubkey      string `json:"identity_pubkey"`
		Alias               string `json:"alias"`
		NumPendingChannels  uint32 `json:"num_pending_channels"`
		NumActiveChannels   uint32 `json:"num_active_channels"`
		NumInactiveChannels uint32 `json:"num_inactive_channels"`
		NumPeers            uint32 `json:"num_peers"`
		BlockHeight         uint32 `json:"block_height"`
		SyncedToChain       bool   `json:"synced_to_chain"`
		SyncedToGraph       bool   `json:"synced_to_graph"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode LND GetInfo response: %w", err)
	}

	return &bitcoinv1alpha1.LightningRuntimeStatus{
		IdentityPubkey:      info.IdentityPubkey,
		Alias:               info.Alias,
		Version:             info.Version,
		BlockHeight:         info.BlockHeight,
		SyncedToChain:       info.SyncedToChain,
		SyncedToGraph:       info.SyncedToGraph,
		NumPeers:            info.NumPeers,
		NumPendingChannels:  info.NumPendingChannels,
		NumActiveChannels:   info.NumActiveChannels,
		NumInactiveChannels: info.NumInactiveChannels,
	}, nil
}

func lightningRPCSecretName(l *bitcoinv1alpha1.LightningNode) string {
	if l.Spec.RPC.SecretName != "" {
		return l.Spec.RPC.SecretName
	}
	return derivedLightningName(l.Name, "-rpc")
}

func lightningRPCPublisherName(l *bitcoinv1alpha1.LightningNode) string {
	return derivedLightningName(l.Name, "-rpc-publisher")
}

func lightningOperatorRPCSecretName(l *bitcoinv1alpha1.LightningNode) string {
	return derivedLightningName(l.Name, "-operator-rpc")
}

func derivedLightningName(name, suffix string) string {
	const maxNameLength = 253
	if len(name)+len(suffix) <= maxNameLength {
		return name + suffix
	}
	return name[:maxNameLength-len(suffix)] + suffix
}

func ensureControlledByLightningNode(obj metav1.Object, l *bitcoinv1alpha1.LightningNode, kind string) error {
	if !metav1.IsControlledBy(obj, l) {
		return fmt.Errorf("%s %s already exists and is not controlled by LightningNode %s", kind, obj.GetName(), l.Name)
	}
	return nil
}

func (r *LightningNodeReconciler) ensureRPCPublishingResources(ctx context.Context, l *bitcoinv1alpha1.LightningNode) error {
	secretName := lightningRPCSecretName(l)
	publisherName := lightningRPCPublisherName(l)

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: l.Namespace}, secret)
	if errors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: l.Namespace, Labels: labelsForLightningNode(l.Name)},
			Type:       corev1.SecretTypeOpaque,
		}
		if err := ctrl.SetControllerReference(l, secret, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, secret); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := ensureControlledByLightningNode(secret, l, "Secret"); err != nil {
		return err
	}

	operatorSecretName := lightningOperatorRPCSecretName(l)
	operatorSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: operatorSecretName, Namespace: l.Namespace}, operatorSecret)
	if errors.IsNotFound(err) {
		operatorSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      operatorSecretName,
				Namespace: l.Namespace,
				Labels:    labelsForLightningNode(l.Name),
				Annotations: map[string]string{
					"bitcoin.kiln-fired.github.io/internal": "true",
				},
			},
			Type: corev1.SecretTypeOpaque,
		}
		if err := ctrl.SetControllerReference(l, operatorSecret, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, operatorSecret); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := ensureControlledByLightningNode(operatorSecret, l, "Secret"); err != nil {
		return err
	}

	serviceAccount := &corev1.ServiceAccount{}
	err = r.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: l.Namespace}, serviceAccount)
	if errors.IsNotFound(err) {
		serviceAccount = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: publisherName, Namespace: l.Namespace}}
		if err := ctrl.SetControllerReference(l, serviceAccount, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, serviceAccount); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := ensureControlledByLightningNode(serviceAccount, l, "ServiceAccount"); err != nil {
		return err
	}

	role := &rbacv1.Role{}
	err = r.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: l.Namespace}, role)
	if errors.IsNotFound(err) {
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: publisherName, Namespace: l.Namespace},
			Rules: []rbacv1.PolicyRule{{
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{secretName, operatorSecretName},
				Verbs:         []string{"get", "update", "patch"},
			}},
		}
		if err := ctrl.SetControllerReference(l, role, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, role); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := ensureControlledByLightningNode(role, l, "Role"); err != nil {
		return err
	}

	roleBinding := &rbacv1.RoleBinding{}
	err = r.Get(ctx, types.NamespacedName{Name: publisherName, Namespace: l.Namespace}, roleBinding)
	if errors.IsNotFound(err) {
		roleBinding = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: publisherName, Namespace: l.Namespace},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     publisherName,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      publisherName,
				Namespace: l.Namespace,
			}},
		}
		if err := ctrl.SetControllerReference(l, roleBinding, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, roleBinding); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := ensureControlledByLightningNode(roleBinding, l, "RoleBinding"); err != nil {
		return err
	}

	return nil
}

func (r *LightningNodeReconciler) statefulsetForLightningNode(l *bitcoinv1alpha1.LightningNode, connection resolvedBitcoinConnection) *appsv1.StatefulSet {
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
	publisherName := lightningRPCPublisherName(l)
	rpcSecretName := lightningRPCSecretName(l)
	operatorRPCSecretName := lightningOperatorRPCSecretName(l)

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
			"--restlisten=0.0.0.0:8080",
			"--listen=0.0.0.0:9735",
			"--tlsextradomain=$(RPCSERVICE)",
			"--tlsdisableautofill",
			"--tlsautorefresh",
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: 9735, Name: "p2p"},
			{ContainerPort: 10009, Name: "rpc"},
			{ContainerPort: 8080, Name: "rest"},
		},
		Env: []corev1.EnvVar{
			{Name: "NETWORK", Value: network},
			{Name: "RPCHOST", Value: connection.Host},
			{Name: "RPCSERVICE", Value: l.Name + "." + l.Namespace + ".svc.cluster.local"},
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

	publisher := corev1.Container{
		Image:   lndInitImage,
		Name:    "rpc-credential-publisher",
		Command: []string{"/bin/sh", "-c"},
		Args: []string{`
while true; do
  CERT=/data/tls.cert
  READONLY=/data/data/chain/bitcoin/$NETWORK/readonly.macaroon
  INVOICE=/data/data/chain/bitcoin/$NETWORK/invoice.macaroon
  ADMIN=/data/data/chain/bitcoin/$NETWORK/admin.macaroon
  if [ -s "$CERT" ] && [ -s "$READONLY" ] && [ -s "$INVOICE" ]; then
    lndinit -v store-secret --batch --overwrite --target=k8s       --k8s.namespace="$POD_NAMESPACE"       --k8s.secret-name="$RPC_SECRET_NAME"       "$CERT" "$READONLY" "$INVOICE"
  fi
  if [ -s "$CERT" ] && [ -s "$ADMIN" ]; then
    lndinit -v store-secret --batch --overwrite --target=k8s       --k8s.namespace="$POD_NAMESPACE"       --k8s.secret-name="$OPERATOR_RPC_SECRET_NAME"       "$CERT" "$ADMIN"
  fi
  sleep 30
done
`},
		Env: []corev1.EnvVar{
			{Name: "NETWORK", Value: network},
			{Name: "RPC_SECRET_NAME", Value: rpcSecretName},
			{Name: "OPERATOR_RPC_SECRET_NAME", Value: operatorRPCSecretName},
			{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
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
		VolumeMounts: []corev1.VolumeMount{{Name: "lnd-data", MountPath: "/data", ReadOnly: true}},
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
					ServiceAccountName:            publisherName,
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
					Containers: []corev1.Container{lnd, publisher},
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
				{Name: "rest", Protocol: corev1.ProtocolTCP, Port: 8080, TargetPort: intstr.FromInt(8080)},
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

func (r *LightningNodeReconciler) mapBitcoinNodeToLightningNodes(ctx context.Context, obj client.Object) []ctrl.Request {
	var nodes bitcoinv1alpha1.LightningNodeList
	if err := r.List(ctx, &nodes,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{lightningNodeBitcoinRefIndex: obj.GetName()},
	); err != nil {
		ctrllog.FromContext(ctx).Error(err, "unable to map BitcoinNode to LightningNodes")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(nodes.Items))
	for i := range nodes.Items {
		requests = append(requests, ctrl.Request{NamespacedName: types.NamespacedName{
			Namespace: nodes.Items[i].Namespace,
			Name:      nodes.Items[i].Name,
		}})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *LightningNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &bitcoinv1alpha1.LightningNode{}, lightningNodeBitcoinRefIndex, func(obj client.Object) []string {
		node := obj.(*bitcoinv1alpha1.LightningNode)
		if node.Spec.BitcoinConnection.NodeRef == "" {
			return nil
		}
		return []string{node.Spec.BitcoinConnection.NodeRef}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.LightningNode{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&bitcoinv1alpha1.BitcoinNode{}, handler.EnqueueRequestsFromMapFunc(r.mapBitcoinNodeToLightningNodes)).
		Complete(r)
}
