module github.com/fi-ts/postgres-controller

go 1.15

require (
	github.com/go-logr/logr v0.3.0
	github.com/metal-stack/firewall-controller v1.0.1
	github.com/metal-stack/v v1.0.2
	github.com/onsi/ginkgo v1.14.2
	github.com/onsi/gomega v1.10.5
	github.com/zalando/postgres-operator v1.5.0
	inet.af/netaddr v0.0.0-20210115183222-bffc12a571f6
	k8s.io/api v0.19.4
	k8s.io/apiextensions-apiserver v0.18.9
	k8s.io/apimachinery v0.19.4
	k8s.io/client-go v11.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.6.4
)

replace k8s.io/client-go => k8s.io/client-go v0.19.4
