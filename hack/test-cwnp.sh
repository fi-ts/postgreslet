#!/bin/bash

# Apply CRD Postgres to control-plane-cluster corresponding to kubeconfig
kubectl --kubeconfig kubeconfig apply -f config/samples/_test_cwnp.yaml

# Wait till CRD ClusterwideNetworkPolicy is available
until kubectl get clusterwidenetworkpolicy -n firewall -o jsonpath='{.items[0]}' > /dev/null 2>&1; do
    echo 'waiting for CRD ClusterwideNetworkPolicy'
    sleep 1s
done

echo -e 'CRD ClusterwideNetworkPolicy creation test \c'

# Check if only one CRD ClusterwideNetworkPolicy is creatd
if kubectl get clusterwidenetworkpolicy -n firewall -o jsonpath='{.items[1]}' > /dev/null 2>&1 ; then
    echo 'failed: more than one CRD ClusterwideNetworkPolicy'
    exit 1
fi

# Check if the name is correct
POLICY_NAME=$(kubectl get clusterwidenetworkpolicy -n firewall -o jsonpath='{.items[0].metadata.name}')
PG_PROJECT_ID=$(kubectl --kubeconfig kubeconfig get postgres -A -o jsonpath='{.items[0].spec.projectID}')
PG_UID=$(kubectl --kubeconfig kubeconfig get postgres -A -o jsonpath='{.items[0].metadata.uid}')
if [ "$POLICY_NAME" != "$PG_PROJECT_ID--$PG_UID" ]; then
    echo 'failed: wrong name'
    exit 1
fi


# Check the CIDR was correctly transferred
POSTGRES_CIDR=$(kubectl --kubeconfig kubeconfig get postgres -A -o jsonpath='{.items[0].spec.accessList.sourceRanges[0]}')
POLICY_CIDR=$(kubectl get clusterwidenetworkpolicy -A -o jsonpath='{.items[0].spec.ingress[0].from[0].cidr}')
if [ "$POLICY_CIDR" != "$POSTGRES_CIDR" ]; then
    echo 'failed: wrong CIDR'
    exit 1
fi

echo 'passed'

# Delete CRD ClusterwideNetworkPolicy
kubectl --kubeconfig kubeconfig delete -f config/samples/_test_cwnp.yaml

# Check if CRD ClusterwideNetworkPolicy is deleted
while kubectl get clusterwidenetworkpolicy -n firewall "$POLICY_NAME" > /dev/null 2>&1; do
    echo 'CRD ClusterwideNetworkPolicy still there'
    sleep 1s
done
echo 'CRD ClusterwideNetworkPolicy deletion test passed'
