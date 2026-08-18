#!/bin/bash

# generate merged yaml file with default operator manifests

set -e -o pipefail

tmp=$(mktemp -d)
operator_version="$1"
output="$2"

cleanup() {
    rm -rf "${tmp}"
}

trap cleanup INT TERM EXIT

if [ -z "$2" ]; then
    >&2 echo "Usage: $0 <operator version> <output yaml file>"
    exit 1
fi

POSTGRES_OPERATOR_URL="https://raw.githubusercontent.com/zalando/postgres-operator/${operator_version}/manifests"

# output all required yaml files  as concatenated manifests, and split
# each manifest into a separate file
(
    for file in ${POSTGRES_OPERATOR_URL}/configmap.yaml \
		  	    ${POSTGRES_OPERATOR_URL}/operator-service-account-rbac.yaml \
			    ${POSTGRES_OPERATOR_URL}/postgres-operator.yaml \
			    ${POSTGRES_OPERATOR_URL}/api-service.yaml; do
	    echo "---"
	    curl -s "$file"
	done
) | yq --split-exp "\"${tmp}/\" + \$index + \".yaml\"" --no-doc

# generate yq's load() statements from generated manifest files
load=""
for yaml in ${tmp}/*; do
    if [ -n "$load" ]; then
        load="$load, "
    fi

    load="$load load(\"${yaml}\")"
done

# generate a list with all generated manifests as items
printf "apiVersion: v1\nkind: List\nitems:\n" | \
    yq ".items += [ ${load}  ]" > "$output"

