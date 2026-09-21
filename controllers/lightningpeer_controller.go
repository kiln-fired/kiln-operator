/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controllers

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
)

const lightningPeerFinalizer = "bitcoin.kiln-fired.github.io/peer-disconnect"

type LightningPeerObservation struct {
	Connected bool
	Address   string
	Inbound   bool
}

// LightningPeerReconciler reconciles durable LND peer connectivity.
type LightningPeerReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ObservePeer    func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningPeerObservation, error)
	ConnectPeer    func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error
	DisconnectPeer func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) error
}

// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningpeers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningpeers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningpeers/finalizers,verbs=update
// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get

func (r *LightningPeerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	peer := &bitcoinv1alpha1.LightningPeer{}
	if err := r.Get(ctx, req.NamespacedName, peer); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !peer.DeletionTimestamp.IsZero() {
		return r.finalizeLightningPeer(ctx, peer)
	}

	if !containsString(peer.Finalizers, lightningPeerFinalizer) {
		peer.Finalizers = append(peer.Finalizers, lightningPeerFinalizer)
		if err := r.Update(ctx, peer); err != nil {
			return ctrl.Result{}, err
		}
	}

	node, secret, ready, err := r.resolveLightningPeerNode(ctx, peer)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		if err := r.Status().Update(ctx, peer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	observe := r.ObservePeer
	if observe == nil {
		observe = observeLightningPeer
	}
	observation, err := observe(ctx, node, peer, secret)
	if err != nil {
		peer.Status.Phase = "Degraded"
		peer.Status.Connected = false
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ObservationFailed",
			Message:            err.Error(),
			ObservedGeneration: peer.Generation,
		})
		if updateErr := r.Status().Update(ctx, peer); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if observation.Connected {
		peer.Status.Phase = "Connected"
		peer.Status.Connected = true
		peer.Status.ObservedAddress = observation.Address
		peer.Status.Inbound = observation.Inbound
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Connected",
			Status:             metav1.ConditionTrue,
			Reason:             "PeerPresent",
			Message:            "LND reports the desired peer as connected",
			ObservedGeneration: peer.Generation,
		})
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "PeerConnected",
			Message:            "Desired Lightning peer connection is satisfied",
			ObservedGeneration: peer.Generation,
		})
		if err := r.Status().Update(ctx, peer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	connect := r.ConnectPeer
	if connect == nil {
		connect = connectLightningPeer
	}
	if err := connect(ctx, node, peer, secret); err != nil {
		peer.Status.Phase = "Degraded"
		peer.Status.Connected = false
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Connected",
			Status:             metav1.ConditionFalse,
			Reason:             "ConnectFailed",
			Message:            err.Error(),
			ObservedGeneration: peer.Generation,
		})
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ConnectFailed",
			Message:            "LND could not establish the desired peer connection",
			ObservedGeneration: peer.Generation,
		})
		if updateErr := r.Status().Update(ctx, peer); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	log.Info("requested Lightning peer connection", "pubkey", peer.Spec.Pubkey, "address", peer.Spec.Address)
	peer.Status.Phase = "Connecting"
	peer.Status.Connected = false
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               "Connected",
		Status:             metav1.ConditionFalse,
		Reason:             "Connecting",
		Message:            "LND accepted the peer connection request",
		ObservedGeneration: peer.Generation,
	})
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Connecting",
		Message:            "Waiting for LND to report the peer as connected",
		ObservedGeneration: peer.Generation,
	})
	if err := r.Status().Update(ctx, peer); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *LightningPeerReconciler) resolveLightningPeerNode(ctx context.Context, peer *bitcoinv1alpha1.LightningPeer) (*bitcoinv1alpha1.LightningNode, *corev1.Secret, bool, error) {
	node := &bitcoinv1alpha1.LightningNode{}
	if err := r.Get(ctx, types.NamespacedName{Name: peer.Spec.NodeRef, Namespace: peer.Namespace}, node); err != nil {
		if errors.IsNotFound(err) {
			peer.Status.Phase = "WaitingForNode"
			peer.Status.Connected = false
			meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
				Type:               "NodeReady",
				Status:             metav1.ConditionFalse,
				Reason:             "LightningNodeNotFound",
				Message:            "Referenced LightningNode does not exist",
				ObservedGeneration: peer.Generation,
			})
			meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "LightningNodeNotFound",
				Message:            "Peer reconciliation is waiting for its LightningNode",
				ObservedGeneration: peer.Generation,
			})
			return node, nil, false, nil
		}
		return nil, nil, false, err
	}

	ready := meta.FindStatusCondition(node.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		peer.Status.Phase = "WaitingForNode"
		peer.Status.Connected = false
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "NodeReady",
			Status:             metav1.ConditionFalse,
			Reason:             "LightningNodeNotReady",
			Message:            "Referenced LightningNode is not ready",
			ObservedGeneration: peer.Generation,
		})
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "LightningNodeNotReady",
			Message:            "Peer reconciliation is waiting for its LightningNode",
			ObservedGeneration: peer.Generation,
		})
		return node, nil, false, nil
	}

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: lightningOperatorRPCSecretName(node), Namespace: node.Namespace}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			peer.Status.Phase = "WaitingForCredentials"
			meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
				Type:               "NodeReady",
				Status:             metav1.ConditionFalse,
				Reason:             "OperatorCredentialsPending",
				Message:            "Waiting for internal LND operator credentials",
				ObservedGeneration: peer.Generation,
			})
			meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "OperatorCredentialsPending",
				Message:            "Peer reconciliation is waiting for internal credentials",
				ObservedGeneration: peer.Generation,
			})
			return node, secret, false, nil
		}
		return nil, nil, false, err
	}
	if len(secret.Data["tls.cert"]) == 0 || len(secret.Data["admin.macaroon"]) == 0 {
		peer.Status.Phase = "WaitingForCredentials"
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "NodeReady",
			Status:             metav1.ConditionFalse,
			Reason:             "OperatorCredentialsPending",
			Message:            "Internal LND operator credentials are incomplete",
			ObservedGeneration: peer.Generation,
		})
		return node, secret, false, nil
	}

	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               "NodeReady",
		Status:             metav1.ConditionTrue,
		Reason:             "LightningNodeReady",
		Message:            "Referenced LightningNode and operator credentials are ready",
		ObservedGeneration: peer.Generation,
	})
	return node, secret, true, nil
}

func (r *LightningPeerReconciler) finalizeLightningPeer(ctx context.Context, peer *bitcoinv1alpha1.LightningPeer) (ctrl.Result, error) {
	if !containsString(peer.Finalizers, lightningPeerFinalizer) {
		return ctrl.Result{}, nil
	}

	node := &bitcoinv1alpha1.LightningNode{}
	if err := r.Get(ctx, types.NamespacedName{Name: peer.Spec.NodeRef, Namespace: peer.Namespace}, node); err != nil {
		if errors.IsNotFound(err) {
			return r.releaseLightningPeerFinalizer(ctx, peer)
		}
		return ctrl.Result{}, err
	}

	ready := meta.FindStatusCondition(node.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		peer.Status.Phase = "Disconnecting"
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "LightningNodeNotReady",
			Message:            "Waiting for LightningNode before disconnecting peer",
			ObservedGeneration: peer.Generation,
		})
		_ = r.Status().Update(ctx, peer)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: lightningOperatorRPCSecretName(node), Namespace: node.Namespace}, secret); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	observe := r.ObservePeer
	if observe == nil {
		observe = observeLightningPeer
	}
	observation, err := observe(ctx, node, peer, secret)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if !observation.Connected {
		return r.releaseLightningPeerFinalizer(ctx, peer)
	}

	disconnect := r.DisconnectPeer
	if disconnect == nil {
		disconnect = disconnectLightningPeer
	}
	if err := disconnect(ctx, node, peer, secret); err != nil {
		peer.Status.Phase = "DisconnectBlocked"
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "DisconnectFailed",
			Message:            err.Error(),
			ObservedGeneration: peer.Generation,
		})
		_ = r.Status().Update(ctx, peer)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return r.releaseLightningPeerFinalizer(ctx, peer)
}

func (r *LightningPeerReconciler) releaseLightningPeerFinalizer(ctx context.Context, peer *bitcoinv1alpha1.LightningPeer) (ctrl.Result, error) {
	peer.Finalizers = removeString(peer.Finalizers, lightningPeerFinalizer)
	if err := r.Update(ctx, peer); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func observeLightningPeer(ctx context.Context, node *bitcoinv1alpha1.LightningNode, peer *bitcoinv1alpha1.LightningPeer, secret *corev1.Secret) (*LightningPeerObservation, error) {
	var response struct {
		Peers []struct {
			Pubkey  string `json:"pub_key"`
			Address string `json:"address"`
			Inbound bool   `json:"inbound"`
		} `json:"peers"`
	}
	if err := doLNDOperatorRequest(ctx, node, secret, http.MethodGet, "/v1/peers", nil, &response); err != nil {
		return nil, err
	}
	for _, observed := range response.Peers {
		if observed.Pubkey == peer.Spec.Pubkey {
			return &LightningPeerObservation{Connected: true, Address: observed.Address, Inbound: observed.Inbound}, nil
		}
	}
	return &LightningPeerObservation{}, nil
}

func connectLightningPeer(ctx context.Context, node *bitcoinv1alpha1.LightningNode, peer *bitcoinv1alpha1.LightningPeer, secret *corev1.Secret) error {
	body := struct {
		Addr struct {
			Pubkey string `json:"pubkey"`
			Host   string `json:"host"`
		} `json:"addr"`
		Perm bool `json:"perm"`
	}{Perm: true}
	body.Addr.Pubkey = peer.Spec.Pubkey
	body.Addr.Host = peer.Spec.Address
	return doLNDOperatorRequest(ctx, node, secret, http.MethodPost, "/v1/peers", body, nil)
}

func disconnectLightningPeer(ctx context.Context, node *bitcoinv1alpha1.LightningNode, peer *bitcoinv1alpha1.LightningPeer, secret *corev1.Secret) error {
	return doLNDOperatorRequest(ctx, node, secret, http.MethodDelete, "/v1/peers/"+url.PathEscape(peer.Spec.Pubkey), nil, nil)
}

func doLNDOperatorRequest(ctx context.Context, node *bitcoinv1alpha1.LightningNode, secret *corev1.Secret, method, path string, requestBody any, responseBody any) error {
	certPEM := secret.Data["tls.cert"]
	macaroon := secret.Data["admin.macaroon"]
	if len(certPEM) == 0 || len(macaroon) == 0 {
		return fmt.Errorf("internal LND operator credentials are incomplete")
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		return fmt.Errorf("unable to parse LND TLS certificate")
	}

	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}

	host := node.Name + "." + node.Namespace + ".svc.cluster.local"
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}},
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+host+":8080"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Grpc-Metadata-macaroon", hex.EncodeToString(macaroon))
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("LND peer request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		response, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("LND peer request returned %s: %s", resp.Status, string(response))
	}
	if responseBody != nil {
		if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
			return fmt.Errorf("decode LND peer response: %w", err)
		}
	}
	return nil
}

func (r *LightningPeerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.LightningPeer{}).
		Complete(r)
}
