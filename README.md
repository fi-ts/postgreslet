# postgres-controller

A small controller which acts as a bridge between the zalando-postgres-operator and our postgres Resource.

## Run an example on two clusters, the remote cluster as the control-cluster and the local kind-cluster as the service-cluster

```bash
# Copy the kubeconfig of the remote cluster to the project folder and name it `kubeconfig`.
cp path/to/remote/cluster/kubeconfig ./kubeconfig

# Create the local service-cluster.
kind create cluster

# Install the dependencies of this project on the remote control-cluster and run the `postgreslet` locally.
make install && make run

# In another terminal, apply the sample-postgres yaml file to the remote control-cluster.
kubectl --kubeconfig kubeconfig apply -f config/samples/database_v1_postgres.yaml

# See the database pods running on the local kind-cluster.
kubectl get pod -A

# Delete the sample-postgres on the remote control-cluster.
kubectl --kubeconfig kubeconfig delete -f config/samples/database_v1_postgres.yaml

# Uninstall the dependencies of this project from the remote control-cluster.
make uninstall
```

## Two clusters. Postgreslet is a k8s Deployment.

```bash
kind create cluster
make configmap
k apply -f https://github.com/jetstack/cert-manager/releases/download/v1.1.0/cert-manager.yaml
make docker-build
make kind-load-image
make deploy
k get pod -A --watch
```
