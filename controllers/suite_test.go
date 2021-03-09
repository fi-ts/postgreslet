/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"log"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	databasev1 "github.com/fi-ts/postgreslet/api/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var ctrlCfg *rest.Config
var ctrlClient client.Client
var ctrlTestEnv *envtest.Environment

var svcCfg *rest.Config
var svcClient client.Client
var svcTestEnv *envtest.Environment

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func(done Done) {
	By("bootstrapping test environment")

	// ctrl cluster
	ctrlTestEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	ctrlCfg, err = ctrlTestEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(ctrlCfg).ToNot(BeNil())

	log.Println(ctrlTestEnv)

	err = databasev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = zalando.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	ctrlClient, err = client.New(ctrlCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(ctrlClient).ToNot(BeNil())

	// svc cluster
	svcTestEnv = &envtest.Environment{}

	svcCfg, err = svcTestEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(svcCfg).ToNot(BeNil())

	log.Println(svcTestEnv)

	err = databasev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = zalando.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	svcClient, err = client.New(svcCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(svcClient).ToNot(BeNil())

	close(done)
}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")

	err := ctrlTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())

	err = svcTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
