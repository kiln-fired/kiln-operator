# Kiln

[![PR CI](https://github.com/kiln-fired/kiln-operator/actions/workflows/pull-request.yaml/badge.svg)](https://github.com/kiln-fired/kiln-operator/actions/workflows/pull-request.yaml)
[![Lightning E2E](https://github.com/kiln-fired/kiln-operator/actions/workflows/lightning-e2e.yaml/badge.svg)](https://github.com/kiln-fired/kiln-operator/actions/workflows/lightning-e2e.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kiln-fired/kiln-operator)](https://goreportcard.com/report/github.com/kiln-fired/kiln-operator)
![Go version](https://img.shields.io/github/go-mod/go-version/kiln-fired/kiln-operator)

Kiln is a Kubernetes operator for running Bitcoin and Lightning infrastructure with explicit lifecycle, persistence, recovery, and credential-management semantics.

The current implementation is intentionally focused rather than generic:

- **Bitcoin:** [btcd](https://github.com/btcsuite/btcd)
- **Lightning:** [LND](https://github.com/lightningnetwork/lnd)
- **Wallet initialization:** [lndinit](https://github.com/lightninglabs/lndinit)
- **Kubernetes API:** namespaced custom resources managed by a controller-runtime operator

Kiln currently optimizes for one thing: making a Bitcoin-backed LND node behave predictably under normal Kubernetes operations such as pod replacement, controller restart, rescheduling, and custom-resource deletion/recreation.

> [!IMPORTANT]
> Kiln is still under active development. The current work has concentrated on lifecycle safety and recovery. Mainnet requires explicit opt-in. Automated backups, seed custody, and declarative payment/channel APIs are not yet part of the supported contract.

## What Kiln manages

Kiln provides three namespaced APIs.

| Resource | Purpose |
| --- | --- |
| `BitcoinNode` | Runs a persistent btcd node and exposes its RPC service |
| `LightningNode` | Runs a persistent LND node backed by a `BitcoinNode` or explicit btcd RPC connection |
| `Seed` | Creates LND-compatible seed material for development/testing workflows |

The primary runtime relationship is:

```text
┌─────────────┐       dependency        ┌───────────────┐
│ BitcoinNode │◄────────────────────────│ LightningNode │
│    btcd     │                         │      LND      │
└──────┬──────┘                         └───────┬───────┘
       │                                        │
       │ PVC                                    │ retained PVC
       │                                        │
       ▼                                        ▼
 blockchain data                    wallet + identity + channels

                                           │
                                           │ publishes
                                           ▼
                                  ┌──────────────────┐
                                  │ RPC client Secret│
                                  │ TLS + macaroons  │
                                  └──────────────────┘
```

## Current runtime baseline

| Component | Default |
| --- | --- |
| Bitcoin | `ghcr.io/btcsuite/btcd:v0.26.2` |
| Lightning | `docker.io/lightninglabs/lnd:v0.21.0-beta` |
| Wallet init | `docker.io/lightninglabs/lndinit:v0.1.36-beta-lnd-v0.21.0-beta` |
| Go | 1.26 |
| Kubernetes libraries | 0.37 |
| controller-runtime | 0.25 |

Container image fields remain overrideable in the custom resources, but Kiln does not currently attempt to abstract across multiple Bitcoin or Lightning implementations.

## Lightning lifecycle contract

Lightning state is not treated like reconstructable Bitcoin chain data. Kiln therefore gives `LightningNode` stronger lifecycle guarantees than a simple Deployment wrapper.

### Persistent identity and state

LND stores its data on a `ReadWriteOncePod` PVC. The StatefulSet uses:

- one replica
- `ReadWriteOncePod` storage fencing
- `OnDelete` update strategy
- retained PVCs when the StatefulSet is deleted or scaled
- a 60-second termination grace period
- graceful `lncli stop` during termination

The wallet is initialized idempotently with `lndinit`. If wallet state already exists on the retained volume, Kiln validates and reuses it rather than creating a new node identity.

### Safe deletion

`LightningNode` uses a finalizer. Deleting the custom resource foreground-deletes the StatefulSet first so LND can stop cleanly.

The LND PVC is deliberately retained.

Deleting and recreating the `LightningNode` with the same name can therefore reattach the retained StatefulSet PVC and recover the same LND identity.

### Stable TLS identity

Kiln adds the Kubernetes Service DNS name to the LND TLS certificate and disables automatic certificate population from transient pod addresses.

The TLS certificate is stored with the persisted LND state. Pod replacement must not rotate the node's self-signed certificate merely because the pod IP changed.

This matters because Kiln publishes that certificate as client trust material.

## Network and mainnet safety

Kiln defaults both Bitcoin and Lightning resources to `simnet`.

Supported network values are:

- `simnet`
- `testnet`
- `regtest`
- `signet`
- `mainnet`

The resolved network is reported in `status.network`, and both node types expose a `NetworkReady` condition.

Mainnet is deliberately different. Kiln will not start a mainnet `BitcoinNode` or `LightningNode` unless that resource explicitly opts in:

```yaml
spec:
  network: mainnet
  safety:
    allowMainnet: true
```

For a `LightningNode`, the network remains under `bitcoinConnection`:

```yaml
spec:
  bitcoinConnection:
    nodeRef: bitcoin-mainnet
    network: mainnet
  safety:
    allowMainnet: true
```

A referenced `BitcoinNode` and `LightningNode` must resolve to the same network. Kiln reports `NetworkReady=False` with reason `NetworkMismatch` and refuses to run LND when they differ.

If a running resource becomes blocked by network policy, Kiln gracefully removes its StatefulSet while retaining the PVC. Restoring a valid policy can therefore recover the same persisted node state.

Kiln also refuses its automatic mining controls on mainnet. `cpuMiningEnabled`, `minBlocks`, and `periodicBlocksEnabled` are development/test-network conveniences and cannot be enabled for a mainnet `BitcoinNode`.

## Bitcoin dependency

A `LightningNode` can reference a same-namespace `BitcoinNode`:

```yaml
spec:
  bitcoinConnection:
    nodeRef: btcd
    network: simnet
```

When `nodeRef` is set, Kiln:

1. waits for the referenced `BitcoinNode` to become `Ready`
2. derives the btcd Service hostname
3. derives the configured btcd RPC TLS Secret
4. derives the btcd RPC username/password Secret references
5. starts LND only after the dependency is usable

Explicit RPC connection fields remain available for externally managed btcd nodes.

## LND client access

Kiln publishes restricted LND client credentials into a Kubernetes Secret.

By default the Secret is named:

```text
<lightning-node-name>-rpc
```

You can choose another name:

```yaml
spec:
  rpc:
    secretName: lnd-rpc
```

The Secret contains:

| Key | Purpose |
| --- | --- |
| `tls.cert` | Trust the LND RPC endpoint |
| `readonly.macaroon` | Read-only LND API access |
| `invoice.macaroon` | Invoice-oriented LND API access |

Kiln intentionally does **not** export `admin.macaroon`.

The credential publisher runs with a dedicated ServiceAccount and an RBAC Role scoped to exactly that node's RPC Secret. It cannot mutate arbitrary Secrets in the namespace.

### Connect with lncli

For a `LightningNode` named `lnd` in namespace `bitcoin`:

```shell
lncli --network simnet \
  --rpcserver lnd.bitcoin.svc.cluster.local:10009 \
  --tlscertpath ./tls.cert \
  --macaroonpath ./readonly.macaroon \
  getinfo
```

Kiln also reports the connection metadata directly:

```shell
kubectl get lightningnode lnd -n bitcoin \
  -o jsonpath='{.status.rpcAddress}{"\n"}{.status.rpcSecretName}{"\n"}'
```

## Runtime status

A ready pod is not sufficient evidence that LND itself is healthy.

Kiln performs an authenticated LND `GetInfo` call and uses that response as the authoritative runtime readiness signal.

A `LightningNode` status includes:

```yaml
status:
  phase: Ready
  rpcAddress: lnd.bitcoin.svc.cluster.local:10009
  rpcSecretName: lnd-rpc
  runtime:
    identityPubkey: 03...
    alias: my-node
    version: 0.21.0-beta
    blockHeight: 123456
    syncedToChain: true
    syncedToGraph: true
    numPeers: 2
    numPendingChannels: 0
    numActiveChannels: 3
    numInactiveChannels: 0
```

The controller exposes these conditions:

| Condition | Meaning |
| --- | --- |
| `BitcoinReady` | The configured Bitcoin backend is available |
| `StorageFenced` | The Lightning volume is restricted to one pod |
| `WalletReady` | The LND wallet is initialized/unlocked |
| `CredentialsReady` | Restricted client TLS/macaroons have been published |
| `RuntimeReady` | Authenticated LND `GetInfo` succeeds |
| `Ready` | The node is usable through its authenticated LND API |

If Kubernetes considers the pod ready but authenticated LND RPC fails, Kiln moves the resource to `Degraded` and sets `Ready=False`.

## Example LightningNode

```yaml
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningNode
metadata:
  name: lnd
spec:
  bitcoinConnection:
    nodeRef: btcd
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
```

Wallet passwords and seed material are referenced through Kubernetes Secrets. Kiln does not place secret values in `LightningNode.spec` or `status`.

See [`config/samples`](config/samples) for complete resource examples.

## Recovery guarantees we test

Kiln has a real-cluster destructive E2E test built on kind.

The test creates a real btcd + LND stack and verifies that the same LND identity survives:

1. normal startup
2. operator restart
3. LND pod deletion/recreation
4. deletion and republishing of the RPC credential Secret
5. complete `LightningNode` deletion
6. `LightningNode` recreation against the retained PVC

The test also confirms that an independent client pod can authenticate to LND through the published Service/Secret before and after recovery.

The destructive E2E is intentionally not run for every repository change. It runs:

- on PRs that modify runtime/operator paths
- nightly on `main`
- manually through `workflow_dispatch`

Superseded E2E runs for the same PR are canceled automatically.

## Local development

### Requirements

- Go 1.26
- Docker with BuildKit
- `kubectl` or `oc`
- access to a Kubernetes cluster for deployment testing

Project tooling such as controller-gen, Kustomize, and setup-envtest is installed into `./bin` by the Makefile.

### Unit and controller tests

```shell
make test
```

Controller tests use controller-runtime `envtest`, so an existing cluster is not required.

### Build

```shell
make build
```

### Regenerate API artifacts

After changing API types:

```shell
make generate
make manifests
```

### Run the controller locally

```shell
make install
make run
```

### Build and deploy an operator image

```shell
export IMG=quay.io/kiln-fired/kiln-operator:dev

make docker-build IMG="$IMG"
make docker-push IMG="$IMG"
make deploy IMG="$IMG"
```

## OLM bundle

```shell
export IMG=quay.io/kiln-fired/kiln-operator:latest
export BUNDLE_IMG=quay.io/kiln-fired/kiln-operator-bundle:latest

make bundle IMG="$IMG" VERSION=0.0.1 DEFAULT_CHANNEL=alpha
make bundle-build BUNDLE_IMG="$BUNDLE_IMG"
make bundle-push BUNDLE_IMG="$BUNDLE_IMG"

operator-sdk bundle validate "$BUNDLE_IMG"
```

For a local bundle install:

```shell
operator-sdk run bundle "$BUNDLE_IMG"
```

## Security model

Current defaults intentionally favor limited authority:

- LND client credentials are Kubernetes Secrets
- wallet passwords and seeds are referenced, not copied into status
- the exported macaroon set excludes admin access
- the credential publisher can update only its own destination Secret
- existing unrelated Secrets are never silently adopted
- LND state uses single-pod storage fencing
- persistent Lightning state is retained rather than silently destroyed

Kiln does not currently provide automated seed custody or external backup storage. Operators remain responsible for the durability and security characteristics of the Kubernetes storage and Secret systems beneath Kiln.

## Scope and roadmap

Kiln's current milestone is a trustworthy Kubernetes substrate for btcd + LND.

Implemented:

- persistent btcd lifecycle
- persistent LND lifecycle
- graceful shutdown/finalizers
- Bitcoin dependency status
- retained Lightning storage
- stable LND identity through recovery
- restricted RPC credential publication
- authenticated runtime status
- explicit network/mainnet guardrails
- cross-resource network consistency
- destructive real-cluster recovery testing

Next areas under consideration:

- imperative Lightning operation APIs
- channel/payment workflows
- external backup/recovery integration
- stronger seed custody models
- additional Bitcoin or Lightning implementations where real requirements justify the abstraction

The project deliberately does not generalize these concepts before there is evidence that the abstraction is useful.
