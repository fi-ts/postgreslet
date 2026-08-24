#!/bin/bash

# Generate a single corev1.List of the zalando operator's default manifests,
# as consumed by pkg/operatormanager.

set -euo pipefail

if [ -z "${2:-}" ]; then
    >&2 echo "Usage: $0 <operator version> <output yaml file>"
    exit 1
fi

url="https://raw.githubusercontent.com/zalando/postgres-operator/$1/manifests"

yq eval-all '[.] | {"apiVersion": "v1", "kind": "List", "items": .}' \
    <(curl -sSf "${url}/configmap.yaml") \
    <(curl -sSf "${url}/operator-service-account-rbac.yaml") \
    <(curl -sSf "${url}/postgres-operator.yaml") \
    <(curl -sSf "${url}/api-service.yaml") \
    > "$2"

