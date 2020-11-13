# postgres-controller
A small controller which act as bridge between zalando-postgres-operator

## Run an example on kind-cluster
```
# Intall zalando dependencies
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