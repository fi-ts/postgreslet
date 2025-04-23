ARG baseImage="golang:1.24"
# Build the manager binary
FROM ${baseImage} as builder

WORKDIR /workspace

# Download dependencies in it's own step to optimize caching during docker build
COPY go.mod .
COPY go.sum .
RUN go mod download all

# Download and cache controller-gen
COPY Makefile .
RUN make controller-gen

COPY . .
RUN make

# Start obj-cache
# https://medium.com/windmill-engineering/tips-tricks-for-making-your-golang-container-builds-10x-faster-4cc618a43827
FROM golang:1.24 as obj-cache
COPY --from=builder /root/.cache /root/.cache

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/manager .
COPY external/svc-postgres-operator.yaml external/svc-postgres-operator.yaml
COPY external/svc-etcd.yaml external/svc-etcd.yaml
USER nonroot:nonroot

ENTRYPOINT ["/manager"]
