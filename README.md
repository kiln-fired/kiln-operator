# `kiln` Operator

![build status](https://github.com/kiln-fired/kiln-operator/workflows/push/badge.svg)
![go report card](https://goreportcard.com/badge/github.com/kiln-fired/kiln-operator)
![go version](https://img.shields.io/github/go-mod/go-version/kiln-fired/kiln-operator)

Kiln is a Kubernetes operator for managing Bitcoin and Lightning node resources.

The project currently provides three namespaced APIs:

- `BitcoinNode` for btcd-backed Bitcoin nodes
- `LightningNode` for LND nodes
- `Seed` for LND-compatible seed material

The current Lightning runtime baseline is upstream `lightninglabs/lnd:v0.21.0-beta` with `lndinit` for idempotent wallet initialization.

### Lightning lifecycle and recovery

A `LightningNode` may reference a same-namespace `BitcoinNode` through `spec.bitcoinConnection.nodeRef`. Kiln waits for the referenced Bitcoin node's `Ready` condition before starting LND and derives the btcd RPC host and secret references from it. The explicit RPC connection fields remain available for externally managed Bitcoin nodes.

Lightning state is treated as non-reconstructable state. The LND data directory is stored on a `ReadWriteOncePod` PVC, the StatefulSet uses an `OnDelete` update strategy, and PVCs are retained when the StatefulSet is deleted or scaled. Deleting a `LightningNode` first foreground-deletes its StatefulSet so LND can perform a graceful `lncli stop`; the retained PVC is intentionally not deleted by the controller.

Wallet seed material and wallet passwords are supplied only through Kubernetes Secret references. The controller does not copy them into the custom resource or status. `lndinit` validates an existing wallet instead of recreating it, so pod replacement and controller restarts reuse the persisted wallet and node identity.

The status exposes `BitcoinReady`, `StorageFenced`, `WalletReady`, `CredentialsReady`, and `Ready` conditions plus a concise lifecycle phase.

### Lightning RPC access

Kiln publishes restricted LND client credentials to a managed Secret after the node becomes available. By default the Secret is named `<lightning-node>-rpc`; set `spec.rpc.secretName` to choose another name. The Secret contains `tls.cert`, `readonly.macaroon`, and `invoice.macaroon`. The admin macaroon is intentionally not exported.

`status.rpcAddress` contains the in-cluster RPC endpoint and `status.rpcSecretName` identifies the credential Secret. LND includes the Kubernetes Service DNS name in its persisted TLS certificate. Kiln deliberately does not enable LND TLS auto-refresh because pod IP changes must not rotate the self-signed certificate and invalidate already-published client trust material.

For example, after copying the Secret values into files inside a client pod:

```shell
lncli --network simnet \
  --rpcserver lnd.default.svc.cluster.local:10009 \
  --tlscertpath ./tls.cert \
  --macaroonpath ./readonly.macaroon \
  getinfo
```

The per-node credential publisher can only update that node's RPC Secret. It cannot read or modify arbitrary Secrets in the namespace.

## Requirements

For local development:

- Go 1.26
- Docker with BuildKit support
- access to a Kubernetes cluster for running the operator
- `kubectl` or `oc` configured for that cluster

Build and test tooling such as `controller-gen`, Kustomize, and `setup-envtest` is installed into `./bin` by the Makefile as needed.

## Development

Run the controller test suite:

```shell
make test
```

Build the manager binary:

```shell
make build
```

When modifying API types, regenerate code and CRD manifests:

```shell
make generate
make manifests
```

The tests use controller-runtime `envtest`, so they do not require an existing Kubernetes cluster.

## Running locally

Install the CRDs into the cluster referenced by your current kubeconfig and run the controller from your workstation:

```shell
make install
make run
```

Sample custom resources are available under [`config/samples`](config/samples).

## Building the operator image

Set the target image and build it:

```shell
export IMG=quay.io/kiln-fired/kiln-operator:latest
make docker-build IMG="$IMG"
```

Push it to the registry:

```shell
docker login quay.io
make docker-push IMG="$IMG"
```

To deploy that image to the current cluster:

```shell
make deploy IMG="$IMG"
```

## OLM bundle

Generate and validate an Operator Lifecycle Manager bundle:

```shell
export IMG=quay.io/kiln-fired/kiln-operator:latest
export BUNDLE_IMG=quay.io/kiln-fired/kiln-operator-bundle:latest

make bundle IMG="$IMG" VERSION=0.0.1 DEFAULT_CHANNEL=alpha
make bundle-build BUNDLE_IMG="$BUNDLE_IMG"
```

Push and validate the bundle image:

```shell
make bundle-push BUNDLE_IMG="$BUNDLE_IMG"
operator-sdk bundle validate "$BUNDLE_IMG"
```

For local bundle installation:

```shell
operator-sdk run bundle "$BUNDLE_IMG"
```

## Project status

Kiln is being modernized from its original 2022-2023 implementation. The current work is preserving existing behavior while updating the operator framework and Bitcoin/Lightning dependency stack. Larger changes to storage, recovery, key custody, and Bitcoin backend architecture are intentionally being handled separately.
