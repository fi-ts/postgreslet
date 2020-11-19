module github.com/fi-ts/postgres-controller

go 1.15

require (
	github.com/go-logr/logr v0.3.0
	github.com/go-logr/zapr v0.3.0 // indirect
	github.com/onsi/ginkgo v1.14.2
	github.com/onsi/gomega v1.10.3
	github.com/zalando/postgres-operator v1.5.0
	k8s.io/api v0.19.3
	k8s.io/apimachinery v0.19.3
	k8s.io/client-go v11.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.6.3
)

replace k8s.io/client-go => k8s.io/client-go v0.19.3

replace github.com/zalando/postgres-operator => github.com/LimKianAn/postgres-operator v1.5.1-0.20201119204833-49ea74bbc92f
