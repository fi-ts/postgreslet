# postgres-controller
A small controller which act as bridge between zalando-postgres-operator

## Run an example on kind-cluster
```
# Intall zalando dependencies.
k apply -k github.com/zalando/postgres-operator/manifests

# Install our dependencies.
make generate && make manifests && make install

make docker-build

# Check the image is ready.
docker image ls

# Load the image to the kind-cluster.
make kind-load-image

make deploy

# todo: Remove this command.
# Something wrong with RBAC. The serviceaccount has already got the right accesses to create API-resources. 
# This command is a work arround.
k create clusterrolebinding controller-is-cluster-admin --clusterrole=cluster-admin --serviceaccount=postgres-controller-system:default

# Apply an example of our CRD Postgres
k apply -f config/samples/database_v1_postgres.yaml
```