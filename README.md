# postgres-controller

A small controller which acts as a bridge between the zalando-postgres-operator and our postgres Resource.

## Run an example on two clusters, one as the control-cluster and the other as the service-cluster

```bash
# Create a local control-cluster. This step is optional if you already have a working kubeconfig/cluster
# IMPORTANT: update the apiServerAddress to your needs so the service-cluster from down below can access the control-cluster.
kind create cluster --name ctrl --kubeconfig ./kubeconfig --config ctrl-cluster-config

# Copy the kubeconfig of the control-cluster to the project folder and name it `kubeconfig`.
# When using kind as describe above, this file was already created
# cp <EXISTING_KUBECONFIG> ./kubeconfig

# Create a local service-cluster. This step is optional if you already have a working kubeconfig/cluster
# This step will now set the kind as current context, which is important for the next step
kind create cluster

# Build and install our CRD in the control-cluster.
# This step uses the "external" kubeconfig we copied to ./kubeconfig earlier. This can be configured in the Makefile
make generate && make manifests && make install

# Run the postgreslet in the service-cluster
# This step uses the current-context of your default kubeconfig (e.g. ~/.kube/config)
make run

# In another terminal, apply the sample-postgres yaml file to the control-cluster.
kubectl --kubeconfig kubeconfig get postgres
kubectl --kubeconfig kubeconfig apply -f config/samples/database_v1_postgres.yaml
kubectl --kubeconfig kubeconfig get postgres --watch

# See the database pods running in the service-cluster.
kubectl get postgresql,pod -A

# Delete the sample-postgres from the control-cluster.
kubectl --kubeconfig kubeconfig delete -f config/samples/database_v1_postgres.yaml

# Uninstall the dependencies of this project from the remote control-cluster.
make uninstall
```

## Install a local kubeconfig as secret in the service-cluster

The following steps will create a _Secret_ called `postgreslet`, and add all files in the folder as keys to that secret.

As we only copy one file, the secret will contain only one key named `controlplane-kubeconfig` which will contain the control plane kube config.

```sh
make secret
```

## Deploy Postgreslet on the local service-cluster and test it

Deploy _postgrelet_ which consumes the _secret_ in the last section.

```sh
make kind-load-image
make deploy
```

Create _postgres_ on control-cluster.

```sh
make install
make create-postgres
```

Delete _postgres_ on control-cluster and all the local corresponding resources.

```sh
make delete-postgres
```
