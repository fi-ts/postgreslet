# postgres-controller
A small controller which act as bridge between zalando-postgres-operator

## Run an example on kind-cluster
```
k apply -k github.com/zalando/postgres-operator/manifests
make && make manifests && make install
k apply -f postgres.crd.yaml
make docker-build

# Check the image is ready
docker image ls
make docker-load-kind
make run

# todo: Make this work.
make deploy
```