
# Image URL to use all building/pushing image targets
IMG ?= r.metal-stack.io/extensions/postgreslet
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:trivialVersions=true"

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# for version informations in the binary
SHA := $(shell git rev-parse --short=8 HEAD)
GITVERSION := $(shell git describe --long --all)
BUILDDATE := $(shell date -Iseconds)
VERSION := $(or ${DOCKER_TAG},latest)

# Postgres operator variables for YAML download
POSTGRES_OPERATOR_VERSION ?= v1.6.0
POSTGRES_OPERATOR_URL ?= https://raw.githubusercontent.com/zalando/postgres-operator/$(POSTGRES_OPERATOR_VERSION)/manifests
POSTGRES_CRD_URL ?= https://raw.githubusercontent.com/zalando/postgres-operator/$(POSTGRES_OPERATOR_VERSION)/charts/postgres-operator/crds/postgresqls.yaml

all: manager

# Run tests
test: generate fmt vet manifests
	go test ./... -coverprofile cover.out

# todo: Modify Dockerfile to include the version magic
# Build manager binary
manager: generate fmt vet
	go build -a -ldflags "-extldflags '-static' \
						-X 'github.com/metal-stack/v.Version=$(VERSION)' \
						-X 'github.com/metal-stack/v.Revision=$(GITVERSION)' \
						-X 'github.com/metal-stack/v.GitSHA1=$(SHA)' \
						-X 'github.com/metal-stack/v.BuildDate=$(BUILDDATE)'" \
	-o bin/manager main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet manifests
	go run ./main.go -partition-id sample-partition -tenant sample-tenant -controlplane-kubeconfig "./kubeconfig"

# Install CRDs into a cluster
install: manifests
	kustomize build config/crd | kubectl --kubeconfig kubeconfig apply -f -

# Uninstall CRDs from a cluster
uninstall: manifests
	kustomize build config/crd | kubectl --kubeconfig kubeconfig delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: manifests secret
	cd config/manager && kustomize edit set image controller=${IMG}:${VERSION}
	kustomize build config/default | kubectl apply -f -

# clean up deployed resources in the configured Kubernetes cluster in ~/.kube/config
cleanup: manifests
	cd config/manager && kustomize edit set image controller=${IMG}:${VERSION}
	kustomize build config/default | kubectl delete -f -

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Generate code
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
docker-build:
	docker build . -t ${IMG}:${VERSION}

# Push the docker image
docker-push:
	docker push ${IMG}:${VERSION}

kind-load-image: docker-build
	kind load docker-image ${IMG}:${VERSION} -v 1

# find or download controller-gen
# download controller-gen if necessary
controller-gen:
ifeq (, $(shell which controller-gen))
	@{ \
	set -e ;\
	CONTROLLER_GEN_TMP_DIR=$$(mktemp -d) ;\
	cd $$CONTROLLER_GEN_TMP_DIR ;\
	go mod init tmp ;\
	go get sigs.k8s.io/controller-tools/cmd/controller-gen@v0.2.5 ;\
	rm -rf $$CONTROLLER_GEN_TMP_DIR ;\
	}
CONTROLLER_GEN=$(GOBIN)/controller-gen
else
CONTROLLER_GEN=$(shell which controller-gen)
endif

# Todo: Fix two metrics-addr. Not read right now.
configmap:
	kubectl create configmap -n system controller-manager-configmap \
		--from-literal=CONTROLPLANE_KUBECONFIG=kubeconfig \
		--from-literal=ENABLE_LEADER_ELECTION=false \
		--from-literal=METRICS_ADDR_CTRL_MGR=8081 \
		--from-literal=METRICS_ADDR_SVC_MGR=8082 \
		--from-literal=PARTITION_ID=sample-partition \
		--from-literal=TENANT=sample-tenant \
		--dry-run=client -o=yaml \
		> config/manager/configmap.yaml

svc-postgres-operator-yaml:
	kubectl apply \
	-f $(POSTGRES_OPERATOR_URL)/configmap.yaml \
	-f $(POSTGRES_OPERATOR_URL)/operator-service-account-rbac.yaml \
	-f $(POSTGRES_OPERATOR_URL)/postgres-operator.yaml \
	-f $(POSTGRES_OPERATOR_URL)/api-service.yaml \
	--dry-run=client -o yaml > external/svc-postgres-operator.yaml

crd-postgresql-yaml:
	kubectl apply -f $(POSTGRES_CRD_URL) --dry-run=client -o yaml > external/crd-postgresql.yaml

secret:
	@{ \
	NS="postgres-controller-system" ;\
	SECRET_DIR="postgreslet-secret" ;\
	kubectl create ns $$NS --dry-run=client --save-config -o yaml | kubectl apply -f - ;\
	if [ -d $$SECRET_DIR ]; then rm -fr $$SECRET_DIR; fi ;\
	mkdir $$SECRET_DIR ;\
	cp kubeconfig $$SECRET_DIR/controlplane-kubeconfig ;\
	kubectl create secret generic postgreslet -n $$NS --from-file $$SECRET_DIR/ --dry-run=client -o yaml | kubectl apply -f - ;\
	}

create-postgres:
	kubectl --kubeconfig kubeconfig apply -f config/samples/database_v1_postgres.yaml

delete-postgres:
	kubectl --kubeconfig kubeconfig delete -f config/samples/database_v1_postgres.yaml
