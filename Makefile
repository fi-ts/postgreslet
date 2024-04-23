
# Image URL to use all building/pushing image targets
IMG ?= r.metal-stack.io/postgreslet

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# for version information in the binary
SHA := $(shell git rev-parse --short=8 HEAD)
GITVERSION := $(shell git describe --long --all)
BUILDDATE := $(shell date -Iseconds)
VERSION := $(or ${DOCKER_TAG},latest)
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
CONTROLLER_TOOLS_VERSION ?= v0.14.0

# Postgres operator variables for YAML download
POSTGRES_OPERATOR_VERSION ?= v1.11.0
POSTGRES_OPERATOR_URL ?= https://raw.githubusercontent.com/zalando/postgres-operator/$(POSTGRES_OPERATOR_VERSION)/manifests
POSTGRES_CRD_URL ?= https://raw.githubusercontent.com/zalando/postgres-operator/$(POSTGRES_OPERATOR_VERSION)/charts/postgres-operator/crds/postgresqls.yaml

# Object cache variables
CACHEOBJ_IMG ?= local/postgreslet-builder-cacheobjs
CACHEOBJ_VERSION ?= previous

all: manager

# Run tests
test: generate fmt vet manifests
	KUBEBUILDER_ASSETS=${GOBIN} go test ./... -coverprofile cover.out -v

# todo: Modify Dockerfile to include the version magic
# Build manager binary
manager: generate fmt vet manifests
	CGO_ENABLED=0 \
	go build -a -ldflags "-extldflags '-static' \
						-X 'github.com/metal-stack/v.Version=$(VERSION)' \
						-X 'github.com/metal-stack/v.Revision=$(GITVERSION)' \
						-X 'github.com/metal-stack/v.GitSHA1=$(SHA)' \
						-X 'github.com/metal-stack/v.BuildDate=$(BUILDDATE)'" \
	-o bin/manager main.go
	strip bin/manager

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet manifests install-configmap-sidecars install-crd-cwnp
	go run ./main.go \
	-partition-id sample-partition \
	-tenant sample-tenant \
	-controlplane-kubeconfig "./kubeconfig" \
	-load-balancer-ip "127.0.0.1" \
	-port-range-start 32000 \
	-port-range-size 8000

# Install CRDs into a cluster
localkube-install-crd: manifests
	kubectl kustomize config/crd | kubectl --kubeconfig kubeconfig-ctrl apply -f -

# # Uninstall CRDs from a cluster
# localkube-uninstall: manifests
# 	kubectl kustomize config/crd | kubectl --kubeconfig kubeconfig-ctrl delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: install-crd-cwnp manifests secret localkube-load-image
	cd config/manager && kubectl kustomize edit set image controller=${IMG}:${VERSION}
	kubectl kustomize config/default | kubectl apply -f -

# clean up deployed resources in the configured Kubernetes cluster in ~/.kube/config
cleanup: manifests
	cd config/manager && kubectl kustomize edit set image controller=${IMG}:${VERSION}
	kubectl kustomize config/default | kubectl delete -f -

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) +crd:generateEmbeddedObjectMeta=true paths="./..." +output:dir=config/crd/bases

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


cacheobjs-daily-base:
	if [ "$(shell docker images ${CACHEOBJ_IMG}:${CACHEOBJ_VERSION} -q)" = "" ]; then \
		docker build -t ${CACHEOBJ_IMG}:${CACHEOBJ_VERSION} -f Dockerfile --target=obj-cache .; \
	fi;

cacheobjs: cacheobjs-daily-base
	$(call inject-nonce)
	docker build --build-arg baseImage=${CACHEOBJ_IMG}:${CACHEOBJ_VERSION} \
               -t ${IMG}:${VERSION} \
               -f Dockerfile .

# Push the docker image
docker-push:
	docker push ${IMG}:${VERSION}

localkube-load-image: cacheobjs
	kind load docker-image --name svc ${IMG}:${VERSION} -v 1

# find or download controller-gen
# download controller-gen if necessary
.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

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

# crd-postgresql-yaml:
# 	kubectl apply -f $(POSTGRES_CRD_URL) --dry-run=client -o yaml > external/crd-postgresql.yaml

secret:
	@{ \
	NS="postgreslet-system" ;\
	SECRET_DIR="postgreslet-secret" ;\
	kubectl create ns $$NS --dry-run=client --save-config -o yaml | kubectl apply -f - ;\
	if [ -d $$SECRET_DIR ]; then rm -fr $$SECRET_DIR; fi ;\
	mkdir $$SECRET_DIR ;\
	cp kubeconfig $$SECRET_DIR/controlplane-kubeconfig ;\
	kubectl create secret generic postgreslet -n $$NS --from-file $$SECRET_DIR/ --dry-run=client -o yaml | kubectl apply -f - ;\
	}

localkube-create-postgres:
	kubectl create ns metal-extension-cloud --dry-run=client --save-config -o yaml | kubectl --kubeconfig kubeconfig-ctrl apply -f -
	kubectl --kubeconfig kubeconfig-ctrl apply -f config/samples/complete.yaml

localkube-delete-postgres:
	kubectl --kubeconfig kubeconfig-ctrl delete -f config/samples/complete.yaml

test-cwnp:
	./hack/test-cwnp.sh

localkube-install-crd-cwnp:
	kubectl apply --kubeconfig ./kubeconfig-svc -f https://raw.githubusercontent.com/metal-stack/firewall-controller/master/config/crd/bases/metal-stack.io_clusterwidenetworkpolicies.yaml
	kubectl create ns firewall --kubeconfig ./kubeconfig-svc --dry-run=client --save-config -o yaml | kubectl apply --kubeconfig ./kubeconfig-svc -f -

# localkube-uninstall-crd-cwnp:
# 	kubectl delete ns firewall
# 	kubectl delete -f https://raw.githubusercontent.com/metal-stack/firewall-controller/master/config/crd/bases/metal-stack.io_clusterwidenetworkpolicies.yaml

# configmap-sidecars:
# 	helm template postgreslet --namespace postgreslet-system charts/postgreslet --show-only templates/configmap-sidecars.yaml > external/test/configmap-sidecars.yaml

# install-configmap-sidecars:
# 	kubectl create ns postgreslet-system --dry-run=client --save-config -o yaml | kubectl apply -f -
# 	kubectl apply -f external/test/configmap-sidecars.yaml

# Todo: Add release version when the changes in main branch are released
crd-cwnp-for-testing:
	curl https://raw.githubusercontent.com/metal-stack/firewall-controller/master/config/crd/bases/metal-stack.io_clusterwidenetworkpolicies.yaml -o external/test/crd-clusterwidenetworkpolicy.yaml

KUBEBUILDER_VERSION:=3.2.0
kubebuilder:
ifeq (,$(wildcard ~/.kubebuilder/${KUBEBUILDER_VERSION}))
	{ \
	os=$$(go env GOOS) ;\
	arch=$$(go env GOARCH) ;\
	curl -L https://go.kubebuilder.io/dl/${KUBEBUILDER_VERSION}/$${os}/$${arch} | tar -xz -C /tmp/ ;\
	mv /tmp/kubebuilder_${KUBEBUILDER_VERSION}_$${os}_$${arch}/bin/* ${GOBIN} ;\
	mkdir -p ~/.kubebuilder ;\
	touch ~/.kubebuilder/${KUBEBUILDER_VERSION} ;\
	}
endif

kubebuilder-version-ci:
	@echo ${KUBEBUILDER_VERSION}

localkube-setup:
	################################################################################
	#                                                                              #
	# Control Cluster                                                              #
	#                                                                              #
	################################################################################
	#
	make localkube-ctrl
	#
	################################################################################
	#                                                                              #
	# Service Cluster                                                              #
	#                                                                              #
	################################################################################
	#
	make localkube-svc

localkube-ctrl:
	kind create cluster --name ctrl --kubeconfig ./kubeconfig-ctrl --image kindest/node:v1.24.15
	container_ip=$$(docker inspect --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 'ctrl-control-plane') ;\
	kubectl --kubeconfig=kubeconfig-ctrl config set-cluster 'kind-ctrl' --server="https://$${container_ip}:6443"
	make localkube-install-crd
	make localkube-create-postgres

localkube-svc:
	kind create cluster --name svc --kubeconfig ./kubeconfig-svc --image kindest/node:v1.24.15
	# make localkube-load-image
	kubectl create ns postgreslet-system --dry-run=client --save-config -o yaml --kubeconfig ./kubeconfig-svc | kubectl apply --kubeconfig ./kubeconfig-svc -f -
	make localkube-install-crd-cwnp
	make localkube-install-crd-servicemonitor
	make localkube-reinstall-postgreslet 

localkube-teardown:
	kind delete cluster --name ctrl
	kind delete cluster --name svc

localkube-install-crd-servicemonitor:
	kubectl apply --kubeconfig ./kubeconfig-svc -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.45.0/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml
	kubectl apply --kubeconfig ./kubeconfig-svc -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.45.0/example/prometheus-operator-crd/monitoring.coreos.com_podmonitors.yaml

localkube-reinstall-postgreslet: localkube-load-image
	# helm repo add metal-stack https://helm.metal-stack.io # stable repo
	# helm upgrade --install postgreslet metal-stack/postgreslet --namespace postgreslet-system --values svc-cluster-values.yaml --set-file controlplaneKubeconfig=kubeconfig 
	helm repo add metal-stack-30 https://helm.metal-stack.io/pull_requests/custom-operator-image # PR repo
	helm upgrade --install postgreslet metal-stack-30/postgreslet --namespace postgreslet-system --values svc-cluster-values.yaml --set-file controlplaneKubeconfig=kubeconfig-ctrl  --kubeconfig ./kubeconfig-svc

lint:
	golangci-lint run -p bugs -p unused --timeout=5m
