# Kiln

[![Main CI](https://github.com/kiln-fired/kiln-operator/actions/workflows/push.yaml/badge.svg?branch=main)](https://github.com/kiln-fired/kiln-operator/actions/workflows/push.yaml?query=branch%3Amain)
[![Lightning E2E](https://github.com/kiln-fired/kiln-operator/actions/workflows/lightning-e2e.yaml/badge.svg?branch=main)](https://github.com/kiln-fired/kiln-operator/actions/workflows/lightning-e2e.yaml?query=branch%3Amain)
![Go version](https://img.shields.io/github/go-mod/go-version/kiln-fired/kiln-operator)

Kiln is a Kubernetes operator for running Bitcoin and Lightning infrastructure with explicit lifecycle, persistence, recovery, and credential-management semantics.

The current implementation is intentionally focused rather than generic:

- **Bitcoin:** [btcd](https://github.com/btcsuite/btcd)
- **Lightning:** [LND](https://github.com/lightningnetwork/lnd)
- **Wallet initialization:** [lndinit](https://github.com/lightninglabs/lndinit)
- **Kubernetes API:** namespaced custom resources managed by a controller-runtime operator

Kiln currently optimizes for one thing: making a Bitcoin-backed LND node behave predictably under normal Kubernetes operations such as pod replacement, controller restart, rescheduling, and custom-resource deletion/recreation.

> [!IMPORTANT]
> Kiln is still under active development. The current work has concentrated on lifecycle safety and recovery. Mainnet requires explicit opt-in. Kiln retains LND's native static channel backup inside Kubernetes, but external backup replication, stronger seed custody, and application-level payment or invoice APIs are not yet part of the supported contract.

## What Kiln manages

Kiln provides namespaced APIs for Bitcoin, Lightning nodes, peers, channels, and seed material.

| Resource | Purpose |
| --- | --- |
| `BitcoinNode` | Runs a persistent btcd node and exposes its RPC service |
| `LightningNode` | Runs a persistent LND node backed by a `BitcoinNode` or explicit btcd RPC connection |
| `LightningPeer` | Declares a peer connection that Kiln continuously reconciles through LND |
| `LightningChannel` | Declares a funded channel that Kiln reconciles through pending, open, and closing states |
| `Seed` | Creates LND-compatible seed material for development/testing workflows |

New node workloads use type-qualified Kubernetes resource names so a `BitcoinNode` and `LightningNode` may safely share the same CR name:

```text
BitcoinNode/foo   -> StatefulSet/foo-bitcoin, Service/foo-bitcoin
LightningNode/foo -> StatefulSet/foo-lightning, Service/foo-lightning
```

Kiln verifies controller ownership before using or deleting an existing child resource. It never adopts an unrelated StatefulSet or Service merely because the name matches. Existing installations remain recovery-compatible: when Kiln finds an owned legacy StatefulSet or a retained legacy PVC, that node continues using the pre-change resource name so upgrades and CR recreation do not strand persisted data.

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

## Bitcoin storage

`BitcoinNode` storage capacity and StorageClass are configurable when the node is provisioned:

```yaml
spec:
  storage:
    size: 1Ti
    storageClassName: fast-storage
```

The defaults are a `2Gi` request and the cluster's default StorageClass. Kiln always uses `ReadWriteOncePod` for the Bitcoin data claim; access mode is a lifecycle-safety invariant rather than a user-selectable setting.

`storage.size` and `storage.storageClassName` are immutable after creation. Kiln does not treat edits to the CR as a generic PVC migration or resize operation. Retained PVCs remain authoritative during deletion/recreation recovery, so changing storage settings cannot silently replace persisted Bitcoin data.

## Bitcoin persistent peers

`BitcoinNode.spec.peers` declares the persistent btcd peers Kiln should manage:

```yaml
spec:
  peers:
    - node-a.example.com:8333
    - node-b.example.com:8333
```

Kiln reconciles this as a desired set using btcd's persistent peer API. Missing desired peers are added, and peers previously managed by Kiln are removed when they leave the list.

Kiln deliberately does **not** treat the entire btcd peer table as owned state. DNS/discovered peers are unaffected, and persistent peers added outside Kiln are left alone. `status.managedPeers` records the persistent peers Kiln has adopted into this contract, and `PeersReady` reports whether the desired set is reconciled.

An empty `spec.peers` therefore means "Kiln manages no static peers", not "disconnect this Bitcoin node from the network."

The former singular `spec.peer` field has been replaced by `spec.peers` in the v1alpha1 API.

## Seed material

`Seed` is a development-oriented helper for producing LND-compatible seed material in a Kubernetes Secret. Sensitive mnemonic and passphrase values are never stored directly in the Seed custom resource.

Generate new seed material:

```yaml
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: Seed
metadata:
  name: lnd
spec:
  secretName: lnd-seed
  network: simnet
```

Import existing aezeed material from another Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: lnd-seed-import
stringData:
  mnemonic: "<24-word aezeed mnemonic>"
  passphrase: "<aezeed passphrase>"
---
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: Seed
metadata:
  name: lnd
spec:
  secretName: lnd-seed
  network: simnet
  import:
    secretName: lnd-seed-import
    mnemonicKey: mnemonic
    passphraseKey: passphrase
```

Kiln publishes `mnemonic`, `passphrase`, and the derived `rootkey` only into the output Secret. That output Secret is intentionally retained independently of the `Seed` CR so deleting and recreating the CR does not garbage-collect recovery material. Kiln marks retained Seed Secrets and refuses to adopt unrelated same-name Secrets.

Existing pre-hardening Seed Secrets that are still controller-owned are migrated in place: Kiln removes the old owner reference, adds the retained-seed marker, and preserves the secret bytes.

`Seed.status.conditions` reports `Ready` and `SecretReady`. Invalid mnemonic/passphrase input, missing import Secrets, and target Secret collisions are reported with non-sensitive reasons and messages. If a previously ready generated Seed Secret is deleted, Kiln reports `SeedMaterialLost` rather than silently generating a different identity. Imported seed output can be republished from its source Secret.

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

### Static channel backup

Kiln continuously copies LND's native `channel.backup` Static Channel Backup (SCB) into a retained Kubernetes Secret named:

```text
<lightning-node-name>-scb
```

The backup Secret is intentionally **not** owned by the `LightningNode`. Deleting and recreating the custom resource therefore does not garbage-collect the recovery artifact. On recreation, Kiln reuses the retained Secret only when it carries Kiln's explicit backup annotation for that node; it will not silently adopt an unrelated Secret with the same name.

`BackupReady=True` means a non-empty SCB has been published. Backup publication is observed independently from node readiness: a temporary backup publication failure does not claim that an otherwise healthy LND node is unusable.

The SCB is an LND recovery artifact, not a replacement for wallet seed custody or durable off-cluster backups. Production operators should replicate the retained Secret into a storage system with failure characteristics independent of the Kubernetes cluster.

## Network and mainnet safety

`BitcoinNode` defaults to `simnet`. A managed `LightningNode` does not declare a second network value: it derives the network from its referenced `BitcoinNode`. An externally managed Bitcoin backend declares the network under `bitcoinConnection.external`.

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

For a managed `LightningNode`, the network is derived from its referenced `BitcoinNode`:

```yaml
spec:
  bitcoinConnection:
    nodeRef: bitcoin-mainnet
  safety:
    allowMainnet: true
```

An external Bitcoin backend declares its network explicitly under `bitcoinConnection.external`. Once a Lightning wallet has resolved a network, Kiln refuses an in-place backend change that would move it to another network.

If a running resource becomes blocked by network policy, Kiln gracefully removes its StatefulSet while retaining the PVC. Restoring a valid policy can therefore recover the same persisted node state.

Kiln also refuses its automatic mining controls on mainnet. `cpuMiningEnabled`, `minBlocks`, and `periodicBlocksEnabled` are development/test-network conveniences and cannot be enabled for a mainnet `BitcoinNode`.

## Bitcoin dependency

A `LightningNode` can reference a same-namespace `BitcoinNode`:

```yaml
spec:
  bitcoinConnection:
    nodeRef: btcd
```

When `nodeRef` is set, Kiln:

1. waits for the referenced `BitcoinNode` to become `Ready`
2. derives the btcd Service hostname
3. derives the configured btcd RPC TLS Secret
4. derives the btcd RPC username/password Secret references
5. starts LND only after the dependency is usable

Externally managed btcd nodes use an explicit alternative:

```yaml
spec:
  bitcoinConnection:
    external:
      host: btcd.example.com:18556
      network: signet
      certSecret: btcd-rpc-tls
      apiAuthSecretName: btcd-rpc-creds
      apiUserSecretKey: username
      apiPasswordSecretKey: password
```

Exactly one of `nodeRef` or `external` is allowed.

## API reference model

Kiln resources form a reference graph, not an ownership tree:

```text
BitcoinNode
    ↑ nodeRef
LightningNode
    ↑ nodeRef
LightningPeer
    ↑ peerRef
LightningChannel
```

References follow five rules:

1. A resource references only its immediate first-class dependency.
2. References are fixed-kind and same-namespace.
3. References do not create Kubernetes ownership between Kiln CRs.
4. Referenced-resource changes enqueue dependents through field indexes and watches.
5. Deletion is blocked only when the protocol requires it. In particular, a `LightningPeer` cannot finish deletion while any `LightningChannel` still references it.

Managed `LightningNode` resources derive Bitcoin network and RPC configuration from `bitcoinConnection.nodeRef`. External Bitcoin backends use the mutually exclusive `bitcoinConnection.external` form.

## Declarative Lightning peers

`LightningPeer` represents durable desired connectivity, not a one-shot `lncli connect` command.

```yaml
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningPeer
metadata:
  name: routing-peer
spec:
  nodeRef: lnd
  pubkey: 02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  address: peer.example.com:9735
```

Kiln observes LND's active peer set before taking action. If the pubkey is already connected, reconciliation is satisfied and no connect request is issued. If it is absent, Kiln requires the referenced Lightning node to be synchronized before asking LND to establish a persistent connection.

A transient `syncedToChain=false` observation does not invalidate a peer that LND still reports as connected. Chain synchronization gates new connection side effects; it does not erase already-observed network state.

`nodeRef` and `pubkey` are immutable because they define the identity of the relationship. The address can be updated and is used the next time a connection needs to be established.

Status includes the observed connection address, whether the connection is inbound, and `NodeReady`, `Connected`, and `Ready` conditions.

Deleting a `LightningPeer` is explicitly blocked while any `LightningChannel` CR still references it. Once those channels are removed, Kiln requests a clean LND disconnect before releasing the peer finalizer.

Peer reconciliation uses a separate LightningNode-owned internal credential Secret. The public RPC Secret remains limited to TLS, read-only, and invoice macaroons and never exposes `admin.macaroon`.

## Declarative Lightning channels

`LightningChannel` represents a channel that should exist through a declared `LightningPeer`. The peer is the channel's immediate dependency and identifies the local `LightningNode`.

```yaml
apiVersion: bitcoin.kiln-fired.github.io/v1alpha1
kind: LightningChannel
metadata:
  name: alice-to-bob
spec:
  peerRef: bob
  capacitySats: 100000
  private: true
  minConfs: 1
```

The CR is the desired state. Kiln observes LND before taking any funding action. If the channel is already pending or open, reconciliation adopts the existing Kiln-owned channel and does not fund another one.

Each funding workflow is tagged inside LND with a memo derived from the `LightningChannel` UID. LND persists that memo in both pending and open channel records. This gives Kiln a durable identity for the exact channel it owns, including across controller restarts or a crash after LND accepted a funding request but before Kubernetes status was updated.

Kiln does not adopt unrelated channels merely because they have the same peer or capacity. Multiple independently managed channels to the same peer are therefore unambiguous.

Before funding a missing channel, Kiln requires:

- the referenced `LightningPeer` to be connected and ready
- the peer's `LightningNode` to be usable and synchronized to Bitcoin
- internal LND operator credentials to be available
- explicit `safety.allowMainnet: true` on a mainnet `LightningChannel`

Once a Kiln-owned channel is pending or open, transient `syncedToChain=false` observations do not make the channel disappear from desired state. Kiln continues observing the existing channel and only uses chain synchronization as a gate for new funding side effects.

Funding parameters are immutable because changing the peer, capacity, privacy, or input-confirmation policy would describe a different channel rather than an in-place update.

Status reports the funding outpoint, remote pubkey, active state, capacity, local and remote balances, privacy, lifecycle phase, and readiness conditions.

Deleting a `LightningChannel` reconciles toward channel absence. Kiln requests a cooperative close and keeps the finalizer until LND no longer reports the channel. A pending-open channel is allowed to finish opening before Kiln attempts the cooperative close. Kiln does not automatically force-close a channel when a cooperative close is blocked.

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
  --rpcserver lnd-lightning.bitcoin.svc.cluster.local:10009 \
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
  rpcAddress: lnd-lightning.bitcoin.svc.cluster.local:10009
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
| `NetworkReady` | The resolved Bitcoin network passes Kiln safety policy and remains compatible with the persisted Lightning wallet |
| `BitcoinReady` | The configured Bitcoin backend is available |
| `StorageFenced` | The Lightning volume is restricted to one pod |
| `WalletReady` | The LND wallet is initialized/unlocked |
| `CredentialsReady` | Restricted client TLS/macaroons have been published |
| `BackupReady` | LND's native `channel.backup` has been published to the retained SCB Secret |
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

The test also confirms that an independent client pod can authenticate to LND through the published Service/Secret before and after recovery. It exercises declarative peer/channel creation, crash-safe channel rediscovery, peer deletion blocking while a channel still depends on it, cooperative channel close, and automatic resumption of the pending peer deletion afterward.

After opening a real channel, the E2E waits for a non-empty retained SCB Secret, verifies it survives an operator restart, and verifies the same Secret survives complete `LightningNode` deletion/recreation alongside the retained LND PVC.

The destructive E2E is intentionally not run for every repository change. It runs:

- on PRs that modify runtime/operator paths
- on `main` pushes that modify those same runtime/operator paths
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
- Seed import values are sourced from Secrets rather than plaintext CR fields
- generated/imported Seed output Secrets are retained across Seed CR deletion
- the exported client macaroon set excludes admin access
- internal administrative credentials are isolated in a LightningNode-owned Secret used by Kiln reconciliation
- the credential publisher can update only its two node-owned destination Secrets
- existing unrelated Secrets, StatefulSets, and Services are never silently adopted
- LND state uses single-pod storage fencing
- persistent Lightning state is retained rather than silently destroyed
- LND static channel backups are continuously published to a retained, node-specific Secret

Kiln does not currently provide automated seed custody or external backup storage. Operators remain responsible for the durability and security characteristics of the Kubernetes storage and Secret systems beneath Kiln.

## Scope and roadmap

Kiln's current milestone is a trustworthy Kubernetes substrate for btcd + LND.

Implemented:

- persistent btcd lifecycle
- declarative persistent Bitcoin peer sets
- persistent LND lifecycle
- graceful shutdown/finalizers
- Bitcoin dependency status
- retained Lightning storage
- stable LND identity through recovery
- restricted RPC credential publication
- authenticated runtime status
- declarative Lightning peer reconciliation
- declarative Lightning channel lifecycle
- crash-safe channel ownership through persisted LND memos
- explicit network/mainnet guardrails
- derived Lightning network identity from the referenced Bitcoin backend
- destructive real-cluster recovery testing
- retained LND static channel backup publication

Next areas under consideration:

- external backup/recovery integration
- stronger seed custody models
- additional Bitcoin or Lightning implementations where real requirements justify the abstraction

The project deliberately does not generalize these concepts before there is evidence that the abstraction is useful.
