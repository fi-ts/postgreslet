module github.com/fi-ts/postgreslet

go 1.16

require (
	github.com/go-logr/logr v0.4.0
	github.com/google/uuid v1.2.0
	github.com/metal-stack/firewall-controller v1.0.9
	github.com/metal-stack/v v1.0.3
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.14.0
	github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring v0.49.0
	github.com/spf13/viper v1.8.1
	github.com/zalando/postgres-operator v1.6.3
	inet.af/netaddr v0.0.0-20210718074554-06ca8145d722
	k8s.io/api v0.22.2
	k8s.io/apiextensions-apiserver v0.21.3
	k8s.io/apimachinery v0.22.2
	k8s.io/client-go v0.21.3
	sigs.k8s.io/controller-runtime v0.9.2
)
