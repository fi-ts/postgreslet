#!/bin/bash
kubectl create ns firewall
kubectl apply -f https://raw.githubusercontent.com/metal-stack/firewall-controller/master/config/crd/bases/metal-stack.io_clusterwidenetworkpolicies.yaml
kubectl --kubeconfig kubeconfig apply -f config/samples/_test_cwnp.yaml
until kubectl get clusterwidenetworkpolicy -n firewall -o jsonpath='{.items[0].metadata.name}' > /dev/null 2>&1; do
    echo 'waiting for cwnp'
    sleep 1s
done
echo 'cwnp is ready'
PG_PROJECT_ID=$(kubectl --kubeconfig kubeconfig get postgres -A -o jsonpath='{.items[0].spec.projectID}')
PG_UID=$(kubectl --kubeconfig kubeconfig get postgres -A -o jsonpath='{.items[0].metadata.uid}')
POLICY_NAME=$(kubectl get clusterwidenetworkpolicy -n firewall -o jsonpath='{.items[0].metadata.name}')
echo -e "===> creation test \c"
if [ "$POLICY_NAME" == "$PG_PROJECT_ID--$PG_UID" ]; then
    echo 'passed'
else
    echo 'failed'
    exit 1
fi
kubectl --kubeconfig kubeconfig delete -f config/samples/_test_cwnp.yaml
while kubectl get clusterwidenetworkpolicy -n firewall "$POLICY_NAME" > /dev/null 2>&1; do
    echo 'cwnp still there'
    sleep 1s
done
echo "===> deletion test passed"
kubectl delete ns firewall
