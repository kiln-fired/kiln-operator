#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-kiln-e2e}"
BITCOIN_NODE="${BITCOIN_NODE:-btcd}"
LIGHTNING_NODE="${LIGHTNING_NODE:-lnd}"
CLIENT_POD="${CLIENT_POD:-lnd-client}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

dump_debug() {
  echo "::group::Kiln E2E debug"
  kubectl get pods,pvc,svc,secrets -A -o wide || true
  kubectl get bitcoinnodes,lightningnodes -A -o yaml || true
  kubectl get events -A --sort-by=.lastTimestamp | tail -100 || true
  kubectl logs -n kiln-operator-system deployment/kiln-operator-controller-manager --all-containers --tail=300 || true
  kubectl logs -n "$NAMESPACE" "$LIGHTNING_NODE-0" -c lnd --tail=200 || true
  kubectl logs -n "$NAMESPACE" "$LIGHTNING_NODE-0" -c rpc-credential-publisher --tail=200 || true
  echo "::endgroup::"
}
trap 'rc=$?; if [[ $rc -ne 0 ]]; then dump_debug; fi; rm -rf "$tmpdir"; exit $rc' EXIT

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
    network: simnet
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
  kubectl exec -n "$NAMESPACE" "$CLIENT_POD" --     lncli --network=simnet       --rpcserver="$LIGHTNING_NODE.$NAMESPACE.svc.cluster.local:10009"       --tlscertpath=/rpc/tls.cert       --macaroonpath=/rpc/readonly.macaroon       getinfo | jq -r '.identity_pubkey'
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

kubectl create namespace "$NAMESPACE"

openssl req -x509 -newkey rsa:2048 -nodes -days 1   -keyout "$tmpdir/btcd.key"   -out "$tmpdir/btcd.crt"   -subj "/CN=$BITCOIN_NODE.$NAMESPACE.svc.cluster.local"   -addext "subjectAltName=DNS:$BITCOIN_NODE,DNS:$BITCOIN_NODE.$NAMESPACE.svc,DNS:$BITCOIN_NODE.$NAMESPACE.svc.cluster.local"

kubectl create secret generic btcd-rpc-tls -n "$NAMESPACE"   --from-file=tls.crt="$tmpdir/btcd.crt"   --from-file=tls.key="$tmpdir/btcd.key"   --from-file=ca.crt="$tmpdir/btcd.crt"
kubectl create secret generic btcd-rpc-creds -n "$NAMESPACE"   --from-literal=username=kiln   --from-literal=password=kiln-e2e-password
kubectl create secret generic alice-wallet -n "$NAMESPACE"   --from-literal=password=kiln-wallet-password
kubectl create secret generic seed -n "$NAMESPACE"   --from-literal=mnemonic='above pioneer library glimpse exhibit analyst monitor holiday boil art ketchup mail hunt since now pattern vacant arch museum tourist brisk come pilot devote'   --from-literal=passphrase=test

cat >"$tmpdir/bitcoin.yaml" <<EOF
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: BitcoinNode
metadata:
  name: $BITCOIN_NODE
  namespace: $NAMESPACE
spec:
  mining:
    cpuMiningEnabled: false
    minBlocks: 0
    periodicBlocksEnabled: false
  rpcServer:
    certSecret: btcd-rpc-tls
    apiAuthSecretName: btcd-rpc-creds
    apiUserSecretKey: username
    apiPasswordSecretKey: password
EOF

write_lightning_manifest

kubectl apply -f "$tmpdir/bitcoin.yaml"
kubectl wait -n "$NAMESPACE" bitcoinnode/"$BITCOIN_NODE" --for=condition=Ready --timeout=180s

kubectl apply -f "$tmpdir/lightning.yaml"
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=240s
wait_for_secret_keys lnd-rpc

rpc_address="$(kubectl get lightningnode -n "$NAMESPACE" "$LIGHTNING_NODE" -o jsonpath='{.status.rpcAddress}')"
rpc_secret="$(kubectl get lightningnode -n "$NAMESPACE" "$LIGHTNING_NODE" -o jsonpath='{.status.rpcSecretName}')"
[[ "$rpc_address" == "$LIGHTNING_NODE.$NAMESPACE.svc.cluster.local:10009" ]]
[[ "$rpc_secret" == "lnd-rpc" ]]

recreate_client
initial_pubkey="$(get_pubkey)"
[[ -n "$initial_pubkey" && "$initial_pubkey" != "null" ]]
echo "Initial LND identity: $initial_pubkey"

echo "Restarting operator"
kubectl rollout restart deployment/kiln-operator-controller-manager -n kiln-operator-system
kubectl rollout status deployment/kiln-operator-controller-manager -n kiln-operator-system --timeout=120s
assert_same_pubkey "$initial_pubkey"

echo "Replacing LND pod"
kubectl delete pod -n "$NAMESPACE" "$LIGHTNING_NODE-0" --wait=true
kubectl wait -n "$NAMESPACE" pod/"$LIGHTNING_NODE-0" --for=condition=Ready --timeout=180s
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=180s
assert_same_pubkey "$initial_pubkey"

echo "Deleting published RPC Secret"
kubectl delete secret -n "$NAMESPACE" lnd-rpc
wait_for_secret_keys lnd-rpc
recreate_client
assert_same_pubkey "$initial_pubkey"

echo "Deleting and recreating LightningNode while retaining its PVC"
pvc_name="lnd-data-$LIGHTNING_NODE-0"
pvc_uid_before="$(kubectl get pvc -n "$NAMESPACE" "$pvc_name" -o jsonpath='{.metadata.uid}')"
kubectl delete pod -n "$NAMESPACE" "$CLIENT_POD" --ignore-not-found --wait=true
kubectl delete -f "$tmpdir/lightning.yaml" --wait=true --timeout=180s

for resource in   "secret/lnd-rpc"   "serviceaccount/lnd-rpc-publisher"   "role/lnd-rpc-publisher"   "rolebinding/lnd-rpc-publisher"; do
  if kubectl get -n "$NAMESPACE" "$resource" >/dev/null 2>&1; then
    kubectl wait -n "$NAMESPACE" --for=delete "$resource" --timeout=60s
  fi
done

kubectl get pvc -n "$NAMESPACE" "$pvc_name" >/dev/null
pvc_uid_after_delete="$(kubectl get pvc -n "$NAMESPACE" "$pvc_name" -o jsonpath='{.metadata.uid}')"
[[ "$pvc_uid_before" == "$pvc_uid_after_delete" ]]

kubectl apply -f "$tmpdir/lightning.yaml"
kubectl wait -n "$NAMESPACE" lightningnode/"$LIGHTNING_NODE" --for=condition=Ready --timeout=240s
wait_for_secret_keys lnd-rpc
pvc_uid_after_recreate="$(kubectl get pvc -n "$NAMESPACE" "$pvc_name" -o jsonpath='{.metadata.uid}')"
[[ "$pvc_uid_before" == "$pvc_uid_after_recreate" ]]

recreate_client
assert_same_pubkey "$initial_pubkey"

echo "Lightning real-cluster E2E passed"
