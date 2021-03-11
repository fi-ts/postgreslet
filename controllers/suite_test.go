/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	cr "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
	firewall "github.com/metal-stack/firewall-controller/api/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctrlClusterCfg     *rest.Config
	ctrlClusterClient  client.Client
	ctrlClusterTestEnv *envtest.Environment

	svcClusterCfg     *rest.Config
	svcClusterClient  client.Client
	svcClusterTestEnv *envtest.Environment

	instance *pg.Postgres
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func(done Done) {
	By("bootstrapping test environment")

	// Create test env for ctrl cluster
	ctrlClusterTestEnv = &envtest.Environment{
		// Path to CRD from this project
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	ctrlClusterCfg, err = ctrlClusterTestEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(ctrlClusterCfg).ToNot(BeNil())

	Expect(pg.AddToScheme(scheme.Scheme)).Should(Succeed())
	Expect(firewall.AddToScheme(scheme.Scheme)).Should(Succeed())
	Expect(zalando.AddToScheme(scheme.Scheme)).Should(Succeed())

	// +kubebuilder:scaffold:scheme

	// Create test env for svc cluster
	svcClusterTestEnv = &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{filepath.Join("..", "external", "crd-postgresql.yaml")},
		},
	}

	svcClusterCfg, err = svcClusterTestEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(svcClusterCfg).ToNot(BeNil())

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

	cr.SetLogger(zap.New(zap.UseDevMode(true)))

	// Todo: OperatorManager should be a reconciler
	opMgr, err := operatormanager.New(
		svcClusterCfg,
		filepath.Join("..", "external", "svc-postgres-operator.yaml"),
		scheme.Scheme,
		cr.Log.WithName("OperatorManager"),
		"test-psp")
	Expect(err).ToNot(HaveOccurred())

	Expect((&PostgresReconciler{
		CtrlClient:      ctrlClusterMgr.GetClient(),
		SvcClient:       svcClusterMgr.GetClient(),
		PartitionID:     "test-partition-id",
		Tenant:          "test-tenant",
		OperatorManager: opMgr,
		LBManager:       lbmanager.New(svcClusterMgr.GetClient(), "127.0.0.1", int32(32000), int32(8000)),
		Log:             cr.Log.WithName("controllers").WithName("Postgres"),
	}).SetupWithManager(ctrlClusterMgr)).Should(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(ctrlClusterMgr.Start(newCxt())).Should(Succeed())
	}()

	ctrlClusterClient = ctrlClusterMgr.GetClient()
	Expect(ctrlClusterClient).ToNot(BeNil())

	Expect((&StatusReconciler{
		CtrlClient: ctrlClusterMgr.GetClient(),
		SvcClient:  svcClusterMgr.GetClient(),
		Log:        cr.Log.WithName("controllers").WithName("Status"),
	}).SetupWithManager(svcClusterMgr)).Should(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(svcClusterMgr.Start(newCxt())).Should(Succeed())
	}()

	svcClusterClient = svcClusterMgr.GetClient()
	Expect(svcClusterClient).ToNot(BeNil())

	// unmarshalPostgres()

	close(done)
}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")

	err := ctrlClusterTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())

	err = svcClusterTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

func newCxt() context.Context {
	return context.Background()
}

func unmarshalPostgres() {
	defer GinkgoRecover()

	bytes, err := os.ReadFile(filepath.Join("..", "config", "samples", "postgres.yaml"))
	Expect(err).ToNot(HaveOccurred())
	Expect(yaml.Unmarshal(bytes, instance)).Should(Succeed())
}
