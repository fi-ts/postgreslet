
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
	go run ./main.go -partition-id sample-partition -tenant sample-tenant

# Install CRDs into a cluster
install: manifests
	kustomize build config/crd | kubectl apply -f -

# Uninstall CRDs from a cluster
uninstall: manifests
	kustomize build config/crd | kubectl delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: manifests
	cd config/manager && kustomize edit set image controller=${IMG}:${VERSION}
	kustomize build config/default | kubectl apply -f -

# Generate manifests e.g. CRD, RBAC etc.
# todo: Remove `sed ...` once the bug in `kubebuilder` is fixed
manifests: controller-gen
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	sed -z 's#spec:#spec:\n  preserveUnknownFields: false#' -i config/crd/bases/database.fits.cloud_postgres.yaml

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

kind-load-image:
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

copy-external-yaml:
	kubectl apply -k github.com/zalando/postgres-operator/manifests --dry-run=client -o yaml > external.yaml
	sed 's/resourceVersion/# resourceVersion/' -i ./external.yaml

generate-crd-postgresql-yaml:
	kubectl apply -k github.com/zalando/postgres-operator/manifests
	kubectl wait --for=condition=ready pod -l name=postgres-operator
	kubectl get crd postgresqls.acid.zalan.do -o yaml > crd-postgresql.yaml
