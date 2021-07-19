# Build the manager binary
FROM golang:1.16 as builder

WORKDIR /workspace
COPY . .
RUN make

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/manager .
COPY external/svc-postgres-operator.yaml external/svc-postgres-operator.yaml
USER nonroot:nonroot

ENTRYPOINT ["/manager"]
