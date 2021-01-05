# postgres-controller

A small controller which act as a bridge between the zalando-postgres-operator and our postgres Resource.

## Run an example on kind-cluster

```bash
# Install zalando dependencies
k apply -k github.com/zalando/postgres-operator/manifests

# Install cert-manager
k apply --validate=false -f https://github.com/jetstack/cert-manager/releases/download/v1.0.4/cert-manager.yaml

# Generate the code and build the image
make generate && make docker-build

# Check the image is ready
docker image ls

# Load the image to the kind-cluster
make kind-load-image

make deploy

# Apply an example of our CRD Postgres
k apply -f config/samples/database_v1_postgres.yaml
```
## Run an example on two clusters, the remote cluster as the control-cluster and the local kind-cluster as the service-cluster

```bash
# Copy the kubeconfig of the remote cluster to the project folder and name it `kubeconfig`.
cp path/to/remote/cluster/kubeconfig ./kubeconfig

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
