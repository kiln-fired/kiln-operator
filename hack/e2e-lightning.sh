#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-kiln-e2e}"
BITCOIN_NODE="${BITCOIN_NODE:-btcd}"
LIGHTNING_NODE="${LIGHTNING_NODE:-lnd}"
SECOND_LIGHTNING_NODE="${SECOND_LIGHTNING_NODE:-lnd-bob}"
BITCOIN_RESOURCE="${BITCOIN_NODE}-bitcoin"
LIGHTNING_RESOURCE="${LIGHTNING_NODE}-lightning"
SECOND_LIGHTNING_RESOURCE="${SECOND_LIGHTNING_NODE}-lightning"
CLIENT_POD="${CLIENT_POD:-lnd-client}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

dump_debug() {
  echo "::group::Kiln E2E debug"
  kubectl get pods,pvc,svc,secrets -A -o wide || true
  kubectl get bitcoinnodes,lightningnodes,lightningpeers,lightningchannels,seeds -A -o yaml || true
  kubectl get events -A --sort-by=.lastTimestamp | tail -100 || true
  kubectl logs -n kiln-operator-system deployment/kiln-operator-controller-manager --all-containers --tail=300 || true
  kubectl logs -n "$NAMESPACE" "$LIGHTNING_RESOURCE-0" -c lnd --tail=200 || true
  kubectl logs -n "$NAMESPACE" "$LIGHTNING_RESOURCE-0" -c rpc-credential-publisher --tail=200 || true
  kubectl logs -n "$NAMESPACE" "$SECOND_LIGHTNING_RESOURCE-0" -c lnd --tail=200 || true
  kubectl logs -n "$NAMESPACE" "$SECOND_LIGHTNING_RESOURCE-0" -c rpc-credential-publisher --tail=200 || true
  echo "::endgroup::"
}
trap 'rc=$?; if [[ $rc -ne 0 ]]; then dump_debug; fi; rm -rf "$tmpdir"; exit $rc' EXIT

wait_for_condition_status() {
  local resource="$1"
  local name="$2"
  local condition="$3"
  local expected="$4"
  local attempts=60
  for ((i=1; i<=attempts; i++)); do
    local actual
    actual="$(kubectl get "$resource" -n "$NAMESPACE" "$name" -o json 2>/dev/null | jq -r --arg condition "$condition" '.status.conditions[]? | select(.type == $condition) | .status' | tail -1)"
    if [[ "$actual" == "$expected" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "Timed out waiting for $resource/$name condition $condition=$expected" >&2
  return 1
}

assert_no_statefulset() {
  local name="$1"
  if kubectl get statefulset -n "$NAMESPACE" "$name" >/dev/null 2>&1; then
    echo "Unexpected StatefulSet $name exists" >&2
    return 1
  fi
}

wait_for_secret_keys() {
  local secret="$1"
  local attempts=90
  for ((i=1; i<=attempts; i++)); do
    if kubectl get secret -n "$NAMESPACE" "$secret" >/dev/null 2>&1; then
      local tls readonly invoice
      tls="$(kubectl get secret -n "$NAMESPACE" "$secret" -o jsonpath='{.data.tls\.cert}' 2>/dev/null || true)"
      readonly="$(kubectl get secret -n "$NAMESPACE" "$secret" -o jsonpath='{.data.readonly\.macaroon}' 2>/dev/null || true)"
      invoice="$(kubectl get secret -n "$NAMESPACE" "$secret" -o jsonpath='{.data.invoice\.macaroon}' 2>/dev/null || true)"
      if [[ -n "$tls" && -n "$readonly" && -n "$invoice" ]]; then
        return 0
      fi
    fi
    sleep 2
  done
  echo "Timed out waiting for RPC credentials in secret $secret" >&2
  return 1
}

write_lightning_manifest() {
  cat >"$tmpdir/lightning.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningNode
metadata:
  name: $LIGHTNING_NODE
  namespace: $NAMESPACE
spec:
  bitcoinConnection:
    nodeRef: $BITCOIN_NODE
  rpc:
    secretName: lnd-rpc
  wallet:
    password:
      secretName: alice-wallet
      secretKey: password
    seed:
      secretName: seed
      mnemonicKey: mnemonic
      passphraseKey: passphrase
EOF
}

write_client_manifest() {
  cat >"$tmpdir/client.yaml" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $CLIENT_POD
  namespace: $NAMESPACE
spec:
  restartPolicy: Never
  containers:
  - name: client
    image: docker.io/lightninglabs/lnd:v0.21.0-beta
    command: ["/bin/sh", "-c", "sleep 3600"]
    volumeMounts:
    - name: rpc
      mountPath: /rpc
      readOnly: true
  volumes:
  - name: rpc
    secret:
      secretName: lnd-rpc
EOF
}

recreate_client() {
  kubectl delete pod -n "$NAMESPACE" "$CLIENT_POD" --ignore-not-found --wait=true
  write_client_manifest
  kubectl apply -f "$tmpdir/client.yaml"
  kubectl wait -n "$NAMESPACE" pod/"$CLIENT_POD" --for=condition=Ready --timeout=120s
}

get_pubkey() {
  kubectl exec -n "$NAMESPACE" "$CLIENT_POD" --     lncli --network=simnet       --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009"       --tlscertpath=/rpc/tls.cert       --macaroonpath=/rpc/readonly.macaroon       getinfo | jq -r '.identity_pubkey'
}

assert_same_pubkey() {
  local expected="$1"
  local actual
  actual="$(get_pubkey)"
  if [[ -z "$actual" || "$actual" == "null" ]]; then
    echo "Unable to read LND identity pubkey" >&2
    return 1
  fi
  if [[ "$actual" != "$expected" ]]; then
    echo "LND identity changed: expected $expected, got $actual" >&2
    return 1
  fi
  echo "Verified LND identity: $actual"
}

wait_for_lightning_sync() {
  local node="$1"
  local attempts=90
  for ((i=1; i<=attempts; i++)); do
    local synced
    synced="$(kubectl get lightningnode -n "$NAMESPACE" "$node" -o jsonpath='{.status.runtime.syncedToChain}' 2>/dev/null || true)"
    if [[ "$synced" == "true" ]]; then
      return 0
    fi
    sleep 2
  done
  echo "Timed out waiting for LightningNode $node to synchronize to Bitcoin" >&2
  return 1
}

mine_to_address() {
  local blocks="$1"
  local address="$2"
  local current target actual configured_address

  configured_address="$(kubectl get secret -n "$NAMESPACE" mining-address -o jsonpath='{.data.address}' | base64 -d)"
  if [[ "$configured_address" != "$address" ]]; then
    kubectl create secret generic mining-address -n "$NAMESPACE" \
      --from-literal=address="$address" \
      --dry-run=client -o yaml | kubectl apply -f -

    echo "Restarting btcd to apply updated mining address"
    kubectl delete pod -n "$NAMESPACE" "$BITCOIN_RESOURCE-0" --wait=true
    kubectl wait -n "$NAMESPACE" pod/"$BITCOIN_RESOURCE-0" --for=condition=Ready --timeout=120s
  fi

  current="$(kubectl get bitcoinnode -n "$NAMESPACE" "$BITCOIN_NODE" -o jsonpath='{.status.LastBlockCount}')"
  target=$((current + blocks))

  kubectl patch bitcoinnode -n "$NAMESPACE" "$BITCOIN_NODE" --type=merge \
    -p "{\"spec\":{\"mining\":{\"minBlocks\":$target}}}" >/dev/null

  for i in {1..120}; do
    actual="$(kubectl get bitcoinnode -n "$NAMESPACE" "$BITCOIN_NODE" -o jsonpath='{.status.LastBlockCount}' 2>/dev/null || true)"
    if [[ "$actual" =~ ^[0-9]+$ ]] && (( actual >= target )); then
      return 0
    fi
    sleep 1
  done

  echo "Timed out waiting for BitcoinNode to mine through block $target" >&2
  return 1
}

kubectl create namespace "$NAMESPACE"

cat >"$tmpdir/mainnet-blocked-bitcoin.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: BitcoinNode
metadata:
  name: blocked-mainnet-bitcoin
  namespace: $NAMESPACE
spec:
  network: mainnet
EOF
kubectl apply -f "$tmpdir/mainnet-blocked-bitcoin.yaml"
wait_for_condition_status bitcoinnode blocked-mainnet-bitcoin NetworkReady False
assert_no_statefulset blocked-mainnet-bitcoin-bitcoin
[[ "$(kubectl get bitcoinnode -n "$NAMESPACE" blocked-mainnet-bitcoin -o jsonpath='{.status.network}')" == "mainnet" ]]
cat >"$tmpdir/mainnet-blocked-lightning.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningNode
metadata:
  name: blocked-mainnet-lightning
  namespace: $NAMESPACE
spec:
  bitcoinConnection:
    external:
      host: example.invalid:18556
      network: mainnet
      certSecret: unused-tls
      apiAuthSecretName: unused-auth
      apiUserSecretKey: username
      apiPasswordSecretKey: password
EOF
kubectl apply -f "$tmpdir/mainnet-blocked-lightning.yaml"
wait_for_condition_status lightningnode blocked-mainnet-lightning NetworkReady False
assert_no_statefulset blocked-mainnet-lightning-lightning
[[ "$(kubectl get lightningnode -n "$NAMESPACE" blocked-mainnet-lightning -o jsonpath='{.status.network}')" == "mainnet" ]]
kubectl delete -f "$tmpdir/mainnet-blocked-lightning.yaml" --wait=true --timeout=60s
kubectl delete -f "$tmpdir/mainnet-blocked-bitcoin.yaml" --wait=true --timeout=60s

openssl req -x509 -newkey rsa:2048 -nodes -days 1   -keyout "$tmpdir/btcd.key"   -out "$tmpdir/btcd.crt"   -subj "/CN=$BITCOIN_RESOURCE.$NAMESPACE.svc.cluster.local"   -addext "subjectAltName=DNS:$BITCOIN_RESOURCE,DNS:$BITCOIN_RESOURCE.$NAMESPACE.svc,DNS:$BITCOIN_RESOURCE.$NAMESPACE.svc.cluster.local"

kubectl create secret generic btcd-rpc-tls -n "$NAMESPACE"   --from-file=tls.crt="$tmpdir/btcd.crt"   --from-file=tls.key="$tmpdir/btcd.key"   --from-file=ca.crt="$tmpdir/btcd.crt"
kubectl create secret generic btcd-rpc-creds -n "$NAMESPACE"   --from-literal=username=kiln   --from-literal=password=kiln-e2e-password
kubectl create secret generic mining-address -n "$NAMESPACE"   --from-literal=address=SNrExv4meNtf7QKvwj5EWudgZUrE14xFqc
kubectl create secret generic alice-wallet -n "$NAMESPACE"   --from-literal=password=kiln-wallet-password
kubectl create secret generic seed -n "$NAMESPACE"   --from-literal=mnemonic='above pioneer library glimpse exhibit analyst monitor holiday boil art ketchup mail hunt since now pattern vacant arch museum tourist brisk come pilot devote'   --from-literal=passphrase=test
kubectl create secret generic bob-wallet -n "$NAMESPACE"   --from-literal=password=kiln-bob-wallet-password

cat >"$tmpdir/bob-seed.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: Seed
metadata:
  name: bob-seed
  namespace: $NAMESPACE
spec:
  secretName: bob-seed-secret
  network: simnet
EOF
kubectl apply -f "$tmpdir/bob-seed.yaml"
for i in {1..60}; do
  kubectl get secret -n "$NAMESPACE" bob-seed-secret >/dev/null 2>&1 && break
  sleep 1
done
kubectl get secret -n "$NAMESPACE" bob-seed-secret >/dev/null

cat >"$tmpdir/bitcoin.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: BitcoinNode
metadata:
  name: $BITCOIN_NODE
  namespace: $NAMESPACE
spec:
  mining:
    cpuMiningEnabled: false
    minBlocks: 1
    periodicBlocksEnabled: false
    rewardAddress:
      secretName: mining-address
      secretKey: address
  rpcServer:
    certSecret: btcd-rpc-tls
    apiAuthSecretName: btcd-rpc-creds
    apiUserSecretKey: username
    apiPasswordSecretKey: password
EOF

write_lightning_manifest

kubectl apply -f "$tmpdir/bitcoin.yaml"
kubectl wait -n "$NAMESPACE" bitcoinnode/"$BITCOIN_NODE" --for=condition=Ready --timeout=180s
[[ "$(kubectl get bitcoinnode -n "$NAMESPACE" "$BITCOIN_NODE" -o jsonpath='{.status.network}')" == "simnet" ]]
[[ "$(kubectl get bitcoinnode -n "$NAMESPACE" "$BITCOIN_NODE" -o jsonpath='{.status.LastBlockCount}')" -ge 1 ]]

kubectl apply -f "$tmpdir/lightning.yaml"
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=240s
wait_for_secret_keys lnd-rpc

alice_address="$(kubectl exec -n "$NAMESPACE" "$LIGHTNING_RESOURCE-0" -c lnd --   lncli --network=simnet     --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009"     --tlscertpath=/data/tls.cert     --macaroonpath=/data/data/chain/bitcoin/simnet/admin.macaroon     newaddress p2wkh | jq -r '.address')"
[[ -n "$alice_address" && "$alice_address" != "null" ]]

echo "Funding Alice's simnet wallet"
mine_to_address 301 "$alice_address"

# A large instantaneous simnet bootstrap can leave LND at the correct tip while
# GetInfo still reports synced_to_chain=false. Restart LND against the now-stable
# chain and require a fresh wallet sync before any peer/channel reconciliation.
echo "Restarting Alice LND after initial simnet bootstrap"
kubectl delete pod -n "$NAMESPACE" "$LIGHTNING_RESOURCE-0" --wait=true
kubectl wait -n "$NAMESPACE" pod/"$LIGHTNING_RESOURCE-0" --for=condition=Ready --timeout=180s
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=180s
wait_for_lightning_sync "$LIGHTNING_NODE"

bitcoin_height="$(kubectl get bitcoinnode -n "$NAMESPACE" "$BITCOIN_NODE" -o jsonpath='{.status.LastBlockCount}')"
(( bitcoin_height > 300 ))

for i in {1..90}; do
  confirmed_balance="$(kubectl exec -n "$NAMESPACE" "$LIGHTNING_RESOURCE-0" -c lnd --     lncli --network=simnet       --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009"       --tlscertpath=/data/tls.cert       --macaroonpath=/data/data/chain/bitcoin/simnet/admin.macaroon       walletbalance | jq -r '.confirmed_balance')"
  if [[ "$confirmed_balance" =~ ^[0-9]+$ ]] && (( confirmed_balance >= 100000 )); then
    break
  fi
  sleep 2
done
[[ "$confirmed_balance" =~ ^[0-9]+$ ]]
(( confirmed_balance >= 100000 ))

[[ -z "$(kubectl get secret -n "$NAMESPACE" lnd-rpc -o jsonpath='{.data.admin\.macaroon}' 2>/dev/null || true)" ]]
for i in {1..90}; do
  operator_admin="$(kubectl get secret -n "$NAMESPACE" "$LIGHTNING_NODE-operator-rpc" -o jsonpath='{.data.admin\.macaroon}' 2>/dev/null || true)"
  [[ -n "$operator_admin" ]] && break
  sleep 2
done
[[ -n "$operator_admin" ]]

cat >"$tmpdir/bob-lightning.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningNode
metadata:
  name: $SECOND_LIGHTNING_NODE
  namespace: $NAMESPACE
spec:
  bitcoinConnection:
    nodeRef: $BITCOIN_NODE
  rpc:
    secretName: bob-rpc
  wallet:
    password:
      secretName: bob-wallet
      secretKey: password
    seed:
      secretName: bob-seed-secret
      mnemonicKey: mnemonic
      passphraseKey: passphrase
EOF
kubectl apply -f "$tmpdir/bob-lightning.yaml"
kubectl wait -n "$NAMESPACE" lightningnode/"$SECOND_LIGHTNING_NODE" --for=condition=Ready --timeout=240s
wait_for_lightning_sync "$SECOND_LIGHTNING_NODE"

bob_pubkey="$(kubectl get lightningnode -n "$NAMESPACE" "$SECOND_LIGHTNING_NODE" -o jsonpath='{.status.runtime.identityPubkey}')"
[[ "$bob_pubkey" =~ ^[0-9a-fA-F]{66}$ ]]

cat >"$tmpdir/peer.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningPeer
metadata:
  name: bob
  namespace: $NAMESPACE
spec:
  nodeRef: $LIGHTNING_NODE
  pubkey: $bob_pubkey
  address: $SECOND_LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:9735
EOF
kubectl apply -f "$tmpdir/peer.yaml"
kubectl wait -n "$NAMESPACE" lightningpeer/bob --for=condition=Ready --timeout=120s
[[ "$(kubectl get lightningpeer -n "$NAMESPACE" bob -o jsonpath='{.status.connected}')" == "true" ]]

rpc_address="$(kubectl get lightningnode -n "$NAMESPACE" "$LIGHTNING_NODE" -o jsonpath='{.status.rpcAddress}')"
rpc_secret="$(kubectl get lightningnode -n "$NAMESPACE" "$LIGHTNING_NODE" -o jsonpath='{.status.rpcSecretName}')"
network="$(kubectl get lightningnode -n "$NAMESPACE" "$LIGHTNING_NODE" -o jsonpath='{.status.network}')"
[[ "$rpc_address" == "$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" ]]
[[ "$rpc_secret" == "lnd-rpc" ]]
[[ "$network" == "simnet" ]]

recreate_client
initial_pubkey="$(get_pubkey)"
[[ -n "$initial_pubkey" && "$initial_pubkey" != "null" ]]
echo "Initial LND identity: $initial_pubkey"

peer_pubkey="$(kubectl exec -n "$NAMESPACE" "$CLIENT_POD" -- lncli --network=simnet --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" --tlscertpath=/rpc/tls.cert --macaroonpath=/rpc/readonly.macaroon listpeers | jq -r --arg pubkey "$bob_pubkey" '.peers[]? | select(.pub_key == $pubkey) | .pub_key')"
[[ "$peer_pubkey" == "$bob_pubkey" ]]

cat >"$tmpdir/channel.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningChannel
metadata:
  name: alice-to-bob
  namespace: $NAMESPACE
spec:
  peerRef: bob
  capacitySats: 100000
  private: true
  minConfs: 1
EOF

echo "Creating declarative Lightning channel"
kubectl apply -f "$tmpdir/channel.yaml"
kubectl wait -n "$NAMESPACE" lightningchannel/alice-to-bob --for=condition=Funded --timeout=180s

channel_point=""
for i in {1..90}; do
  channel_point="$(kubectl get lightningchannel -n "$NAMESPACE" alice-to-bob -o jsonpath='{.status.channelPoint}' 2>/dev/null || true)"
  [[ -n "$channel_point" ]] && break
  sleep 2
done
[[ -n "$channel_point" ]]

echo "Confirming channel funding transaction"
mine_to_address 6 "$alice_address"
kubectl wait -n "$NAMESPACE" lightningchannel/alice-to-bob --for=condition=Ready --timeout=180s

echo "Waiting for retained static channel backup publication"
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=BackupReady --timeout=120s
backup_secret="$LIGHTNING_NODE-scb"
for i in {1..60}; do
  backup_data="$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.data.channel\.backup}' 2>/dev/null || true)"
  [[ -n "$backup_data" ]] && break
  sleep 2
done
[[ -n "$backup_data" ]]
backup_secret_uid="$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.metadata.uid}')"
[[ "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.metadata.ownerReferences}' 2>/dev/null || true)" == "" || "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.metadata.ownerReferences}' 2>/dev/null || true)" == "<no value>" ]]

channel_point_before="$(kubectl get lightningchannel -n "$NAMESPACE" alice-to-bob -o jsonpath='{.status.channelPoint}')"
[[ "$channel_point_before" == "$channel_point" ]]
[[ "$(kubectl get lightningchannel -n "$NAMESPACE" alice-to-bob -o jsonpath='{.status.active}')" == "true" ]]

channel_count="$(kubectl exec -n "$NAMESPACE" "$CLIENT_POD" -- lncli --network=simnet --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" --tlscertpath=/rpc/tls.cert --macaroonpath=/rpc/readonly.macaroon listchannels | jq --arg point "$channel_point_before" '[.channels[]? | select(.channel_point == $point)] | length')"
[[ "$channel_count" == "1" ]]

echo "Restarting operator"
kubectl rollout restart deployment/kiln-operator-controller-manager -n kiln-operator-system
kubectl rollout status deployment/kiln-operator-controller-manager -n kiln-operator-system --timeout=120s
assert_same_pubkey "$initial_pubkey"
kubectl wait -n "$NAMESPACE" lightningpeer/bob --for=condition=Ready --timeout=120s
kubectl wait -n "$NAMESPACE" lightningchannel/alice-to-bob --for=condition=Ready --timeout=120s
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=BackupReady --timeout=120s
[[ "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.metadata.uid}')" == "$backup_secret_uid" ]]
[[ -n "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.data.channel\.backup}')" ]]
[[ "$(kubectl get lightningchannel -n "$NAMESPACE" alice-to-bob -o jsonpath='{.status.channelPoint}')" == "$channel_point_before" ]]

peer_pubkey="$(kubectl exec -n "$NAMESPACE" "$CLIENT_POD" -- lncli --network=simnet --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" --tlscertpath=/rpc/tls.cert --macaroonpath=/rpc/readonly.macaroon listpeers | jq -r --arg pubkey "$bob_pubkey" '.peers[]? | select(.pub_key == $pubkey) | .pub_key')"
[[ "$peer_pubkey" == "$bob_pubkey" ]]

channel_count="$(kubectl exec -n "$NAMESPACE" "$CLIENT_POD" -- lncli --network=simnet --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" --tlscertpath=/rpc/tls.cert --macaroonpath=/rpc/readonly.macaroon listchannels | jq --arg point "$channel_point_before" '[.channels[]? | select(.channel_point == $point)] | length')"
[[ "$channel_count" == "1" ]]

echo "Verifying LightningPeer deletion is blocked by the channel dependency"
kubectl delete -f "$tmpdir/peer.yaml" --wait=false
for i in {1..60}; do
  peer_phase="$(kubectl get lightningpeer -n "$NAMESPACE" bob -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  [[ "$peer_phase" == "DependencyBlocked" ]] && break
  sleep 1
done
[[ "$peer_phase" == "DependencyBlocked" ]]

echo "Deleting LightningChannel and verifying cooperative close"
kubectl delete -f "$tmpdir/channel.yaml" --wait=false
for i in {1..60}; do
  channel_phase="$(kubectl get lightningchannel -n "$NAMESPACE" alice-to-bob -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  [[ "$channel_phase" == "Closing" || "$channel_phase" == "CloseBlocked" ]] && break
  sleep 1
done
[[ "$channel_phase" == "Closing" ]]

mine_to_address 6 "$alice_address"
kubectl wait -n "$NAMESPACE" --for=delete lightningchannel/alice-to-bob --timeout=180s

channel_count="$(kubectl exec -n "$NAMESPACE" "$CLIENT_POD" -- lncli --network=simnet --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" --tlscertpath=/rpc/tls.cert --macaroonpath=/rpc/readonly.macaroon listchannels | jq --arg point "$channel_point_before" '[.channels[]? | select(.channel_point == $point)] | length')"
[[ "$channel_count" == "0" ]]

echo "Waiting for blocked LightningPeer deletion to resume after channel removal"
kubectl wait -n "$NAMESPACE" --for=delete lightningpeer/bob --timeout=120s
for i in {1..60}; do
  peer_pubkey="$(kubectl exec -n "$NAMESPACE" "$CLIENT_POD" -- lncli --network=simnet --rpcserver="$LIGHTNING_RESOURCE.$NAMESPACE.svc.cluster.local:10009" --tlscertpath=/rpc/tls.cert --macaroonpath=/rpc/readonly.macaroon listpeers | jq -r --arg pubkey "$bob_pubkey" '.peers[]? | select(.pub_key == $pubkey) | .pub_key')"
  [[ -z "$peer_pubkey" ]] && break
  sleep 1
done
[[ -z "$peer_pubkey" ]]

echo "Replacing LND pod"
kubectl delete pod -n "$NAMESPACE" "$LIGHTNING_RESOURCE-0" --wait=true
kubectl wait -n "$NAMESPACE" pod/"$LIGHTNING_RESOURCE-0" --for=condition=Ready --timeout=180s
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=180s
assert_same_pubkey "$initial_pubkey"

echo "Deleting published RPC Secret"
kubectl delete secret -n "$NAMESPACE" lnd-rpc
wait_for_secret_keys lnd-rpc
recreate_client
assert_same_pubkey "$initial_pubkey"

echo "Deleting and recreating LightningNode while retaining its PVC"
pvc_name="lnd-data-$LIGHTNING_RESOURCE-0"
pvc_uid_before="$(kubectl get pvc -n "$NAMESPACE" "$pvc_name" -o jsonpath='{.metadata.uid}')"
kubectl delete pod -n "$NAMESPACE" "$CLIENT_POD" --ignore-not-found --wait=true
kubectl delete -f "$tmpdir/lightning.yaml" --wait=true --timeout=180s

for resource in   "secret/lnd-rpc"   "secret/lnd-operator-rpc"   "serviceaccount/lnd-rpc-publisher"   "role/lnd-rpc-publisher"   "rolebinding/lnd-rpc-publisher"; do
  if kubectl get -n "$NAMESPACE" "$resource" >/dev/null 2>&1; then
    kubectl wait -n "$NAMESPACE" --for=delete "$resource" --timeout=60s
  fi
done

kubectl get pvc -n "$NAMESPACE" "$pvc_name" >/dev/null
kubectl get secret -n "$NAMESPACE" "$backup_secret" >/dev/null
[[ "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.metadata.uid}')" == "$backup_secret_uid" ]]
[[ -n "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.data.channel\.backup}')" ]]
pvc_uid_after_delete="$(kubectl get pvc -n "$NAMESPACE" "$pvc_name" -o jsonpath='{.metadata.uid}')"
[[ "$pvc_uid_before" == "$pvc_uid_after_delete" ]]

kubectl apply -f "$tmpdir/lightning.yaml"
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=240s
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=BackupReady --timeout=120s
wait_for_secret_keys lnd-rpc
[[ "$(kubectl get secret -n "$NAMESPACE" "$backup_secret" -o jsonpath='{.metadata.uid}')" == "$backup_secret_uid" ]]
pvc_uid_after_recreate="$(kubectl get pvc -n "$NAMESPACE" "$pvc_name" -o jsonpath='{.metadata.uid}')"
[[ "$pvc_uid_before" == "$pvc_uid_after_recreate" ]]

recreate_client
assert_same_pubkey "$initial_pubkey"

echo "Lightning real-cluster E2E passed"
