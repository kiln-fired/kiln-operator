# `kiln` Operator

A Kubernetes operator for managing the state of Bitcoin and Lightning nodes.

## System Requirements

1. A Kubernetes client and compatible cluster
2. The Go programming language `v1.16`
3. Docker `v17.03+`

From a fresh Fedora 35 install:

`sudo dnf install golang kubernetes-client`

## Development

This operator follows the conventions describe in the [Operator SDK Go Tutorial](https://sdk.operatorframework.io/docs/building-operators/golang/tutorial/).

When modifying resource type definitions, run the following command to generate code for the modified resource:

`make generate`

To generate CRD manifests, run:

`make manifests`

## Running the operator locally

Authenticate to a Kubernetes cluster with as an administrator and run:

`make install run`
