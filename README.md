# postgres-controller

A small controller which acts as a bridge between the zalando-postgres-operator and our postgres Resource.

## Run an example on two clusters, one as the control-cluster and the other as the service-cluster

```bash
# Create a local control-cluster. This step is optional if you already have a working kubeconfig/cluster
kind create cluster --name ctrl

# Copy the kubeconfig of the control-cluster to the project folder and name it `kubeconfig`.
# When using kind as describe above, this file now uses our newly created kind-ctrl Cluster as current-context
cp ~/.kube/config ./kubeconfig

# Create a local service-cluster. This step is optional if you already have a working kubeconfig/cluster
# This step will no set the kind-svc as current context, which is important for the next step
kind create cluster --name svc

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

## Install a local kubeconfig as secret in the cluster

The following steps will create a _Secret_ called `postgreslet`, and add all files in the folder as keys to that secret.

As we only copy one file, the secret will contain only one key named `controlplane-kubeconfig` which will contain the control plane kube config.

```sh
make docker-build
make kind-load-image

kubectl create ns postgres-controller-system

mkdir postgreslet-secret
cp kubeconfig postgreslet-secret/controlplane-kubeconfig
kubectl create secret generic postgreslet --from-file postgreslet-secret/ --dry-run=client -o yaml | kubectl apply -n postgres-controller-system -f -

kubectl apply -f https://github.com/jetstack/cert-manager/releases/download/v1.1.0/cert-manager.yaml

# Wait till the pods of cert-manager are ready.
make deploy
```