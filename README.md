# `kiln` Operator

A Kubernetes operator for managing the state of Bitcoin and Lightning nodes.

## System Requirements

1. A Kubernetes client and compatible cluster
2. The Go programming language `v1.16`
3. Docker `v17.03+`

From a fresh Fedora 35 install:

```shell
sudo dnf install golang kubernetes-client
````

## Development

This operator follows the conventions describe in the [Operator SDK Go Tutorial](https://sdk.operatorframework.io/docs/building-operators/golang/tutorial/).

When modifying resource type definitions, run the following command to generate code for the modified resource:

```shell
make generate
````

To generate CRD manifests, run:

```shell
make manifests
````

## Running the operator locally

Authenticate to a Kubernetes cluster as an administrator and run:

```shell
make install run
````

See [sample CRs](config/samples) for reference configurations.

## Building/Pushing the operator image

```shell
export repo=kiln-fired #replace with yours
docker login quay.io/$repo
make docker-build IMG=quay.io/$repo/kiln-operator:latest
make docker-push IMG=quay.io/$repo/kiln-operator:latest
```

## Deploy to OLM via bundle

```shell
make manifests
make bundle IMG=quay.io/$repo/kiln-operator:latest
# TODO: needs icon
operator-sdk bundle validate ./bundle --select-optional name=operatorhub
make bundle-build BUNDLE_IMG=quay.io/$repo/kiln-operator-bundle:latest
docker push quay.io/$repo/kiln-operator-bundle:latest
# TODO: needs icon
operator-sdk bundle validate quay.io/$repo/kiln-operator-bundle:latest --select-optional name=operatorhub
oc new-project kiln-operator
oc label namespace kiln-operator openshift.io/cluster-monitoring="true"
operator-sdk cleanup kiln-operator -n kiln-operator
operator-sdk run bundle --install-mode AllNamespaces -n kiln-operator quay.io/$repo/kiln-operator-bundle:latest
```