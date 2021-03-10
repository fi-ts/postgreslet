/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"context"
	"log"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	cr "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	databasev1 "github.com/fi-ts/postgreslet/api/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var ctrlClusterCfg *rest.Config
var ctrlClusterClient client.Client
var ctrlClusterTestEnv *envtest.Environment

var svcClusterCfg *rest.Config
var svcClusterClient client.Client
var svcClusterTestEnv *envtest.Environment

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func(done Done) {
	By("bootstrapping test environment")

	// ctrl cluster
	ctrlClusterTestEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	ctrlClusterCfg, err = ctrlClusterTestEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(ctrlClusterCfg).ToNot(BeNil())

	log.Println("ctrlClusterTestEnv: ", ctrlClusterTestEnv)

	err = databasev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = zalando.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	// svc cluster
	svcClusterTestEnv = &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{filepath.Join("..", "external")},
		},
	}

	svcClusterCfg, err = svcClusterTestEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(svcClusterCfg).ToNot(BeNil())

	log.Println("svcClusterTestEnv: ", svcClusterTestEnv)

	err = databasev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = zalando.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	ctrlClusterMgr, err := cr.NewManager(ctrlClusterCfg, cr.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())
	Expect(ctrlClusterMgr).ToNot(BeNil())

	svcClusterMgr, err := cr.NewManager(svcClusterCfg, cr.Options{
		MetricsBindAddress: "0",
		Scheme:             scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())
	Expect(svcClusterMgr).ToNot(BeNil())

	err = (&PostgresReconciler{
		CtrlClient: ctrlClusterMgr.GetClient(),
		SvcClient:  svcClusterMgr.GetClient(),
		Log:        cr.Log.WithName("controllers").WithName("Postgres"),
	}).SetupWithManager(ctrlClusterMgr)
	Expect(err).ToNot(HaveOccurred())

	ctx := context.Background()
	go func() {
		defer GinkgoRecover()
		err = ctrlClusterMgr.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

	ctrlClusterClient = ctrlClusterMgr.GetClient()
	Expect(ctrlClusterClient).ToNot(BeNil())

	err = (&StatusReconciler{
		CtrlClient: ctrlClusterMgr.GetClient(),
		SvcClient:  svcClusterMgr.GetClient(),
		Log:        cr.Log.WithName("controllers").WithName("Status"),
	}).SetupWithManager(svcClusterMgr)
	Expect(err).ToNot(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err := svcClusterMgr.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

	svcClusterClient = svcClusterMgr.GetClient()
	Expect(svcClusterClient).ToNot(BeNil())

	close(done)
}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")

	err := ctrlClusterTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())

	err = svcClusterTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
