/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

const lightningChannelFinalizer = "bitcoin.kiln-fired.github.io/channel-close"

type LightningChannelObservation struct {
	State             string
	ChannelPoint      string
	RemotePubkey      string
	Active            bool
	CapacitySats      int64
	LocalBalanceSats  int64
	RemoteBalanceSats int64
	Private           bool
}

type LightningChannelOpenResult struct {
	ChannelPoint string
}

// LightningChannelReconciler reconciles durable LND channel state.
type LightningChannelReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ObserveChannel func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *corev1.Secret) (*LightningChannelObservation, error)
	OpenChannel    func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *bitcoinv1alpha1.LightningPeer, *corev1.Secret) (*LightningChannelOpenResult, error)
	CloseChannel   func(context.Context, *bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningChannel, *corev1.Secret, string) error
}

// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningchannels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningchannels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningchannels/finalizers,verbs=update
// +kubebuilder:rbac:groups=bitcoin.kiln-fired.github.io,resources=lightningnodes;lightningpeers,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get

func (r *LightningChannelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)
	channel := &bitcoinv1alpha1.LightningChannel{}
	if err := r.Get(ctx, req.NamespacedName, channel); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !channel.DeletionTimestamp.IsZero() {
		return r.finalizeLightningChannel(ctx, channel)
	}

	if !containsString(channel.Finalizers, lightningChannelFinalizer) {
		channel.Finalizers = append(channel.Finalizers, lightningChannelFinalizer)
		if err := r.Update(ctx, channel); err != nil {
			return ctrl.Result{}, err
		}
	}

	node, peer, secret, ready, err := r.resolveLightningChannelDependencies(ctx, channel)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		if err := r.Status().Update(ctx, channel); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	observe := r.ObserveChannel
	if observe == nil {
		observe = observeLightningChannel
	}
	observation, err := observe(ctx, node, channel, secret)
	if err != nil {
		channel.Status.Phase = "Degraded"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ObservationFailed",
			Message:            err.Error(),
			ObservedGeneration: channel.Generation,
		})
		if updateErr := r.Status().Update(ctx, channel); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	r.applyLightningChannelObservation(channel, observation)
	switch observation.State {
	case "Open":
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Funded",
			Status:             metav1.ConditionTrue,
			Reason:             "ChannelOpen",
			Message:            "LND reports the Kiln-owned channel as open",
			ObservedGeneration: channel.Generation,
		})
		if !observation.Active {
			channel.Status.Phase = "Inactive"
			meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "ChannelInactive",
				Message:            "Channel is open but LND does not currently consider it active",
				ObservedGeneration: channel.Generation,
			})
			if err := r.Status().Update(ctx, channel); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		channel.Status.Phase = "Open"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "ChannelActive",
			Message:            "Desired Lightning channel exists and is active",
			ObservedGeneration: channel.Generation,
		})
		if err := r.Status().Update(ctx, channel); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

	case "PendingOpen":
		channel.Status.Phase = "PendingOpen"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Funded",
			Status:             metav1.ConditionTrue,
			Reason:             "FundingPending",
			Message:            "Channel funding transaction exists and is awaiting activation",
			ObservedGeneration: channel.Generation,
		})
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "FundingPending",
			Message:            "Waiting for the funding transaction to activate the channel",
			ObservedGeneration: channel.Generation,
		})
		if err := r.Status().Update(ctx, channel); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case "Closing":
		channel.Status.Phase = "ClosingExternally"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ChannelClosing",
			Message:            "The Kiln-owned channel is closing; a replacement will not be opened until closure completes",
			ObservedGeneration: channel.Generation,
		})
		if err := r.Status().Update(ctx, channel); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	open := r.OpenChannel
	if open == nil {
		open = openLightningChannel
	}
	result, err := open(ctx, node, channel, peer, secret)
	if err != nil {
		channel.Status.Phase = "Degraded"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Funded",
			Status:             metav1.ConditionFalse,
			Reason:             "OpenFailed",
			Message:            err.Error(),
			ObservedGeneration: channel.Generation,
		})
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "OpenFailed",
			Message:            "LND could not establish the desired channel",
			ObservedGeneration: channel.Generation,
		})
		if updateErr := r.Status().Update(ctx, channel); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	log.Info("requested Lightning channel funding", "peer", peer.Spec.Pubkey, "capacitySats", channel.Spec.CapacitySats)
	channel.Status.Phase = "PendingOpen"
	if result != nil && result.ChannelPoint != "" {
		channel.Status.ChannelPoint = result.ChannelPoint
	}
	channel.Status.RemotePubkey = peer.Spec.Pubkey
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               "Funded",
		Status:             metav1.ConditionTrue,
		Reason:             "FundingBroadcast",
		Message:            "LND accepted the channel funding request",
		ObservedGeneration: channel.Generation,
	})
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "FundingPending",
		Message:            "Waiting for the funding transaction to activate the channel",
		ObservedGeneration: channel.Generation,
	})
	if err := r.Status().Update(ctx, channel); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *LightningChannelReconciler) resolveLightningChannelDependencies(ctx context.Context, channel *bitcoinv1alpha1.LightningChannel) (*bitcoinv1alpha1.LightningNode, *bitcoinv1alpha1.LightningPeer, *corev1.Secret, bool, error) {
	peer := &bitcoinv1alpha1.LightningPeer{}
	if err := r.Get(ctx, types.NamespacedName{Name: channel.Spec.PeerRef, Namespace: channel.Namespace}, peer); err != nil {
		if errors.IsNotFound(err) {
			r.setChannelWaiting(channel, "PeerReady", "LightningPeerNotFound", "Referenced LightningPeer does not exist")
			return nil, peer, nil, false, nil
		}
		return nil, nil, nil, false, err
	}

	peerReady := meta.FindStatusCondition(peer.Status.Conditions, "Ready")
	if !peer.DeletionTimestamp.IsZero() || peerReady == nil || peerReady.Status != metav1.ConditionTrue || !peer.Status.Connected {
		r.setChannelWaiting(channel, "PeerReady", "LightningPeerNotReady", "Referenced LightningPeer is not connected and ready")
		return nil, peer, nil, false, nil
	}
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               "PeerReady",
		Status:             metav1.ConditionTrue,
		Reason:             "LightningPeerReady",
		Message:            "Referenced LightningPeer is connected",
		ObservedGeneration: channel.Generation,
	})

	node := &bitcoinv1alpha1.LightningNode{}
	if err := r.Get(ctx, types.NamespacedName{Name: peer.Spec.NodeRef, Namespace: channel.Namespace}, node); err != nil {
		if errors.IsNotFound(err) {
			r.setChannelWaiting(channel, "PeerReady", "LightningPeerDependencyUnavailable", "Referenced LightningPeer's LightningNode does not exist")
			return node, peer, nil, false, nil
		}
		return nil, nil, nil, false, err
	}

	nodeReady := meta.FindStatusCondition(node.Status.Conditions, "Ready")
	if nodeReady == nil || nodeReady.Status != metav1.ConditionTrue || !node.Status.Runtime.SyncedToChain {
		r.setChannelWaiting(channel, "PeerReady", "LightningPeerDependencyNotReady", "Referenced LightningPeer's LightningNode is not ready and chain-synchronized")
		return node, peer, nil, false, nil
	}

	if node.Status.Network == "mainnet" && !channel.Spec.Safety.AllowMainnet {
		channel.Status.Phase = "NetworkBlocked"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "NetworkReady",
			Status:             metav1.ConditionFalse,
			Reason:             "MainnetOptInRequired",
			Message:            "mainnet channel funding requires spec.safety.allowMainnet=true",
			ObservedGeneration: channel.Generation,
		})
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "MainnetOptInRequired",
			Message:            "Channel funding is blocked by mainnet safety policy",
			ObservedGeneration: channel.Generation,
		})
		return node, peer, nil, false, nil
	}
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               "NetworkReady",
		Status:             metav1.ConditionTrue,
		Reason:             "PolicyAccepted",
		Message:            "Channel network safety policy accepted",
		ObservedGeneration: channel.Generation,
	})

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: lightningOperatorRPCSecretName(node), Namespace: node.Namespace}, secret); err != nil {
		if errors.IsNotFound(err) {
			r.setChannelWaiting(channel, "PeerReady", "LightningPeerDependencyNotReady", "Waiting for the referenced peer's internal LND operator credentials")
			return node, peer, secret, false, nil
		}
		return nil, nil, nil, false, err
	}
	if len(secret.Data["tls.cert"]) == 0 || len(secret.Data["admin.macaroon"]) == 0 {
		r.setChannelWaiting(channel, "PeerReady", "LightningPeerDependencyNotReady", "Referenced peer's internal LND operator credentials are incomplete")
		return node, peer, secret, false, nil
	}
	return node, peer, secret, true, nil
}

func (r *LightningChannelReconciler) setChannelWaiting(channel *bitcoinv1alpha1.LightningChannel, conditionType, reason, message string) {
	channel.Status.Phase = "WaitingForDependency"
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: channel.Generation,
	})
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: channel.Generation,
	})
}

func (r *LightningChannelReconciler) applyLightningChannelObservation(channel *bitcoinv1alpha1.LightningChannel, observation *LightningChannelObservation) {
	if observation == nil {
		return
	}
	if observation.ChannelPoint != "" {
		channel.Status.ChannelPoint = observation.ChannelPoint
	}
	if observation.RemotePubkey != "" {
		channel.Status.RemotePubkey = observation.RemotePubkey
	}
	channel.Status.Active = observation.Active
	channel.Status.CapacitySats = observation.CapacitySats
	channel.Status.LocalBalanceSats = observation.LocalBalanceSats
	channel.Status.RemoteBalanceSats = observation.RemoteBalanceSats
	channel.Status.Private = observation.Private
}

func lightningChannelMemo(channel *bitcoinv1alpha1.LightningChannel) string {
	return "kiln-channel/" + string(channel.UID)
}

func observeLightningChannel(ctx context.Context, node *bitcoinv1alpha1.LightningNode, channel *bitcoinv1alpha1.LightningChannel, secret *corev1.Secret) (*LightningChannelObservation, error) {
	memo := lightningChannelMemo(channel)

	var openResponse struct {
		Channels []struct {
			Active        bool   `json:"active"`
			RemotePubkey  string `json:"remote_pubkey"`
			ChannelPoint  string `json:"channel_point"`
			Capacity      string `json:"capacity"`
			LocalBalance  string `json:"local_balance"`
			RemoteBalance string `json:"remote_balance"`
			Private       bool   `json:"private"`
			Memo          string `json:"memo"`
		} `json:"channels"`
	}
	if err := doLNDOperatorRequest(ctx, node, secret, http.MethodGet, "/v1/channels", nil, &openResponse); err != nil {
		return nil, err
	}
	for _, observed := range openResponse.Channels {
		if observed.Memo == memo || (channel.Status.ChannelPoint != "" && observed.ChannelPoint == channel.Status.ChannelPoint) {
			return &LightningChannelObservation{
				State:             "Open",
				ChannelPoint:      observed.ChannelPoint,
				RemotePubkey:      observed.RemotePubkey,
				Active:            observed.Active,
				CapacitySats:      parseLNDInt64(observed.Capacity),
				LocalBalanceSats:  parseLNDInt64(observed.LocalBalance),
				RemoteBalanceSats: parseLNDInt64(observed.RemoteBalance),
				Private:           observed.Private,
			}, nil
		}
	}

	type pendingChannel struct {
		RemotePubkey  string `json:"remote_node_pub"`
		ChannelPoint  string `json:"channel_point"`
		Capacity      string `json:"capacity"`
		LocalBalance  string `json:"local_balance"`
		RemoteBalance string `json:"remote_balance"`
		Private       bool   `json:"private"`
		Memo          string `json:"memo"`
	}
	var pendingResponse struct {
		PendingOpenChannels []struct {
			Channel pendingChannel `json:"channel"`
		} `json:"pending_open_channels"`
		PendingClosingChannels []struct {
			Channel pendingChannel `json:"channel"`
		} `json:"pending_closing_channels"`
		WaitingCloseChannels []struct {
			Channel pendingChannel `json:"channel"`
		} `json:"waiting_close_channels"`
		PendingForceClosingChannels []struct {
			Channel pendingChannel `json:"channel"`
		} `json:"pending_force_closing_channels"`
	}
	if err := doLNDOperatorRequest(ctx, node, secret, http.MethodGet, "/v1/channels/pending", nil, &pendingResponse); err != nil {
		return nil, err
	}

	for _, item := range pendingResponse.PendingOpenChannels {
		if item.Channel.Memo == memo || (channel.Status.ChannelPoint != "" && item.Channel.ChannelPoint == channel.Status.ChannelPoint) {
			return pendingChannelObservation("PendingOpen", item.Channel), nil
		}
	}
	for _, item := range pendingResponse.PendingClosingChannels {
		if item.Channel.Memo == memo || (channel.Status.ChannelPoint != "" && item.Channel.ChannelPoint == channel.Status.ChannelPoint) {
			return pendingChannelObservation("Closing", item.Channel), nil
		}
	}
	for _, item := range pendingResponse.WaitingCloseChannels {
		if item.Channel.Memo == memo || (channel.Status.ChannelPoint != "" && item.Channel.ChannelPoint == channel.Status.ChannelPoint) {
			return pendingChannelObservation("Closing", item.Channel), nil
		}
	}
	for _, item := range pendingResponse.PendingForceClosingChannels {
		if item.Channel.Memo == memo || (channel.Status.ChannelPoint != "" && item.Channel.ChannelPoint == channel.Status.ChannelPoint) {
			return pendingChannelObservation("Closing", item.Channel), nil
		}
	}

	return &LightningChannelObservation{State: "Missing"}, nil
}

func pendingChannelObservation(state string, observed struct {
	RemotePubkey  string `json:"remote_node_pub"`
	ChannelPoint  string `json:"channel_point"`
	Capacity      string `json:"capacity"`
	LocalBalance  string `json:"local_balance"`
	RemoteBalance string `json:"remote_balance"`
	Private       bool   `json:"private"`
	Memo          string `json:"memo"`
}) *LightningChannelObservation {
	return &LightningChannelObservation{
		State:             state,
		ChannelPoint:      observed.ChannelPoint,
		RemotePubkey:      observed.RemotePubkey,
		CapacitySats:      parseLNDInt64(observed.Capacity),
		LocalBalanceSats:  parseLNDInt64(observed.LocalBalance),
		RemoteBalanceSats: parseLNDInt64(observed.RemoteBalance),
		Private:           observed.Private,
	}
}

func openLightningChannel(ctx context.Context, node *bitcoinv1alpha1.LightningNode, channel *bitcoinv1alpha1.LightningChannel, peer *bitcoinv1alpha1.LightningPeer, secret *corev1.Secret) (*LightningChannelOpenResult, error) {
	body := struct {
		NodePubkeyString   string `json:"node_pubkey_string"`
		LocalFundingAmount string `json:"local_funding_amount"`
		Private            bool   `json:"private"`
		MinConfs           int32  `json:"min_confs"`
		SpendUnconfirmed   bool   `json:"spend_unconfirmed"`
		Memo               string `json:"memo"`
	}{
		NodePubkeyString:   peer.Spec.Pubkey,
		LocalFundingAmount: strconv.FormatInt(channel.Spec.CapacitySats, 10),
		Private:            channel.Spec.Private,
		MinConfs:           channel.Spec.MinConfs,
		SpendUnconfirmed:   channel.Spec.SpendUnconfirmed,
		Memo:               lightningChannelMemo(channel),
	}

	var response struct {
		FundingTxidStr string `json:"funding_txid_str"`
		OutputIndex    uint32 `json:"output_index"`
	}
	if err := doLNDOperatorRequest(ctx, node, secret, http.MethodPost, "/v1/channels", body, &response); err != nil {
		return nil, err
	}
	result := &LightningChannelOpenResult{}
	if response.FundingTxidStr != "" {
		result.ChannelPoint = fmt.Sprintf("%s:%d", response.FundingTxidStr, response.OutputIndex)
	}
	return result, nil
}

func closeLightningChannel(ctx context.Context, node *bitcoinv1alpha1.LightningNode, channel *bitcoinv1alpha1.LightningChannel, secret *corev1.Secret, channelPoint string) error {
	txid, index, ok := strings.Cut(channelPoint, ":")
	if !ok || txid == "" || index == "" {
		return fmt.Errorf("invalid channel point %q", channelPoint)
	}
	if _, err := strconv.ParseUint(index, 10, 32); err != nil {
		return fmt.Errorf("invalid channel output index %q: %w", index, err)
	}
	path := fmt.Sprintf("/v1/channels/%s/%s?force=false", txid, index)
	return doLNDOperatorRequest(ctx, node, secret, http.MethodDelete, path, nil, nil)
}

func parseLNDInt64(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func (r *LightningChannelReconciler) finalizeLightningChannel(ctx context.Context, channel *bitcoinv1alpha1.LightningChannel) (ctrl.Result, error) {
	if !containsString(channel.Finalizers, lightningChannelFinalizer) {
		return ctrl.Result{}, nil
	}

	peer := &bitcoinv1alpha1.LightningPeer{}
	if err := r.Get(ctx, types.NamespacedName{Name: channel.Spec.PeerRef, Namespace: channel.Namespace}, peer); err != nil {
		if errors.IsNotFound(err) {
			channel.Status.Phase = "CloseBlocked"
			meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "LightningPeerNotFound",
				Message:            "Cannot safely close channel because its referenced LightningPeer no longer exists",
				ObservedGeneration: channel.Generation,
			})
			_ = r.Status().Update(ctx, channel)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	node := &bitcoinv1alpha1.LightningNode{}
	if err := r.Get(ctx, types.NamespacedName{Name: peer.Spec.NodeRef, Namespace: channel.Namespace}, node); err != nil {
		if errors.IsNotFound(err) {
			channel.Status.Phase = "CloseBlocked"
			meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "LightningNodeNotFound",
				Message:            "Cannot safely close channel because the peer's LightningNode no longer exists",
				ObservedGeneration: channel.Generation,
			})
			_ = r.Status().Update(ctx, channel)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	ready := meta.FindStatusCondition(node.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		channel.Status.Phase = "Closing"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "LightningNodeNotReady",
			Message:            "Waiting for the referenced peer's LightningNode before closing channel",
			ObservedGeneration: channel.Generation,
		})
		_ = r.Status().Update(ctx, channel)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: lightningOperatorRPCSecretName(node), Namespace: node.Namespace}, secret); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	observe := r.ObserveChannel
	if observe == nil {
		observe = observeLightningChannel
	}
	observation, err := observe(ctx, node, channel, secret)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	r.applyLightningChannelObservation(channel, observation)

	switch observation.State {
	case "Missing":
		return r.releaseLightningChannelFinalizer(ctx, channel)
	case "PendingOpen":
		channel.Status.Phase = "ClosingPendingOpen"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "FundingPending",
			Message:            "Waiting for pending channel funding before cooperative close",
			ObservedGeneration: channel.Generation,
		})
		_ = r.Status().Update(ctx, channel)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	case "Closing":
		channel.Status.Phase = "Closing"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ClosePending",
			Message:            "Channel close is pending confirmation",
			ObservedGeneration: channel.Generation,
		})
		_ = r.Status().Update(ctx, channel)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	closeChannel := r.CloseChannel
	if closeChannel == nil {
		closeChannel = closeLightningChannel
	}
	if err := closeChannel(ctx, node, channel, secret, observation.ChannelPoint); err != nil {
		channel.Status.Phase = "CloseBlocked"
		meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "CloseFailed",
			Message:            err.Error(),
			ObservedGeneration: channel.Generation,
		})
		_ = r.Status().Update(ctx, channel)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	channel.Status.Phase = "Closing"
	meta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "CloseRequested",
		Message:            "LND accepted cooperative channel close request",
		ObservedGeneration: channel.Generation,
	})
	_ = r.Status().Update(ctx, channel)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *LightningChannelReconciler) releaseLightningChannelFinalizer(ctx context.Context, channel *bitcoinv1alpha1.LightningChannel) (ctrl.Result, error) {
	channel.Finalizers = removeString(channel.Finalizers, lightningChannelFinalizer)
	if err := r.Update(ctx, channel); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *LightningChannelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bitcoinv1alpha1.LightningChannel{}).
		Complete(r)
}
