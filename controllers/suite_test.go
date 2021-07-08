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
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	coreosv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	cr "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	pg "github.com/fi-ts/postgreslet/api/v1"
	"github.com/fi-ts/postgreslet/pkg/lbmanager"
	"github.com/fi-ts/postgreslet/pkg/operatormanager"
	firewall "github.com/metal-stack/firewall-controller/api/v1"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

const (
	// duration = time.Second * 10
	interval = time.Second * 2
	timeout  = time.Second * 180
)

var (
	ctrlClusterCfg     *rest.Config
	ctrlClusterClient  client.Client
	ctrlClusterTestEnv *envtest.Environment

	svcClusterCfg     *rest.Config
	svcClusterClient  client.Client
	svcClusterTestEnv *envtest.Environment

	externalYAMLDir     = filepath.Join("..", "external")
	externalYAMLDirTest = filepath.Join(externalYAMLDir, "test")

	HelmCRDDir = filepath.Join("..", "charts", "postgreslet", "crds")

	instance = &pg.Postgres{}
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func() {
	done := make(chan interface{})
	go func() {
		defer close(done)

		By("bootstrapping test environment")

		// Create test env for ctrl cluster and start it
		ctrlClusterTestEnv = &envtest.Environment{
			// Path to CRD from this project
			CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
		}
		ctrlClusterCfg = startTestEnv(ctrlClusterTestEnv)

		// Create test env for svc cluster and start it
		svcClusterTestEnv = &envtest.Environment{
			CRDInstallOptions: envtest.CRDInstallOptions{
				Paths: []string{
					filepath.Join(HelmCRDDir, "postgresql.yaml"),
					filepath.Join(externalYAMLDirTest, "crd-clusterwidenetworkpolicy.yaml"),
					filepath.Join(externalYAMLDirTest, "crd-servicemonitors.yaml"),
				},
			},
		}
		svcClusterCfg = startTestEnv(svcClusterTestEnv)

		scheme := newScheme()
		ctrlClusterMgr, err := cr.NewManager(ctrlClusterCfg, cr.Options{Scheme: scheme})
		Expect(err).ToNot(HaveOccurred())
		Expect(ctrlClusterMgr).ToNot(BeNil())

		svcClusterMgr, err := cr.NewManager(svcClusterCfg, cr.Options{
			MetricsBindAddress: "0",
			Scheme:             scheme,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(svcClusterMgr).ToNot(BeNil())

		cr.SetLogger(zap.New(zap.UseDevMode(true)))

		// Todo: OperatorManager should be a reconciler
		opMgr, err := operatormanager.New(
			svcClusterCfg,
			filepath.Join(externalYAMLDir, "svc-postgres-operator.yaml"),
			scheme,
			cr.Log.WithName("OperatorManager"),
			operatormanager.Options{PspName: "test-psp"})
		Expect(err).ToNot(HaveOccurred())

		Expect((&PostgresReconciler{
			CtrlClient:      ctrlClusterMgr.GetClient(),
			SvcClient:       svcClusterMgr.GetClient(),
			PartitionID:     "sample-partition",
			Tenant:          "sample-tenant",
			OperatorManager: opMgr,
			LBManager:       lbmanager.New(svcClusterMgr.GetClient(), &lbmanager.Options{LBIP: "127.0.0.1", PortRangeStart: int32(32000), PortRangeSize: int32(8000)}),
			Log:             cr.Log.WithName("controllers").WithName("Postgres"),
		}).SetupWithManager(ctrlClusterMgr)).Should(Succeed())

		go startMgr(ctrlClusterMgr)

		ctrlClusterClient = ctrlClusterMgr.GetClient()
		Expect(ctrlClusterClient).ToNot(BeNil())

		Expect((&StatusReconciler{
			CtrlClient: ctrlClusterMgr.GetClient(),
			SvcClient:  svcClusterMgr.GetClient(),
			Log:        cr.Log.WithName("controllers").WithName("Status"),
		}).SetupWithManager(svcClusterMgr)).Should(Succeed())

		go startMgr(svcClusterMgr)

		svcClusterClient = svcClusterMgr.GetClient()
		Expect(svcClusterClient).ToNot(BeNil())

		createNamespace(svcClusterClient, "firewall")
		createPostgresTestInstance()
		createConfigMapSidecarConfig()
		createCredentialSecrets()
	}()
	Eventually(done, 1000).Should(BeClosed())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")

	err := ctrlClusterTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())

	err = svcClusterTestEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

func createCredentialSecrets() {
	defer GinkgoRecover()

	users := []string{"postgres", "standby"}
	for i := range users {
		bytes, err := os.ReadFile(filepath.Join(externalYAMLDirTest, string("secret-credential-"+users[i]+".yaml")))
		Expect(err).ToNot(HaveOccurred())
		s := &core.Secret{}
		Expect(yaml.Unmarshal(bytes, s)).Should(Succeed())

		s.Namespace = instance.ToPeripheralResourceNamespace()
		s.Name = users[i] + "." + instance.Name + ".credentials"
		s.Labels = instance.ToZalandoPostgresqlMatchingLabels()
		Eventually(func() bool {
			return svcClusterClient.Create(newCtx(), s) == nil
		}, timeout, interval).Should(BeTrue())
	}
}

func createConfigMapSidecarConfig() {
	defer GinkgoRecover()

	nsObj := &core.Namespace{}
	nsObj.Name = "postgreslet-system"
	Expect(svcClusterClient.Create(newCtx(), nsObj)).Should(Succeed())

	bytes, err := os.ReadFile(filepath.Join(externalYAMLDirTest, "configmap-sidecars.yaml"))
	Expect(err).ToNot(HaveOccurred())

	cm := &core.ConfigMap{}
	Expect(yaml.Unmarshal(bytes, cm)).Should(Succeed())

	Expect(svcClusterClient.Create(newCtx(), cm)).Should(Succeed())
}

func createNamespace(client client.Client, ns string) {
	defer GinkgoRecover()

	nsObj := &core.Namespace{}
	nsObj.Name = ns
	Expect(client.Create(newCtx(), nsObj)).Should(Succeed())
}

func createPostgresTestInstance() {
	defer GinkgoRecover()

	bytes, err := os.ReadFile(filepath.Join("..", "config", "samples", "complete.yaml"))
	Expect(err).ToNot(HaveOccurred())
	Expect(yaml.Unmarshal(bytes, instance)).Should(Succeed())

	createNamespace(ctrlClusterClient, instance.Namespace)

	Expect(ctrlClusterClient.Create(newCtx(), instance)).Should(Succeed())
}

func newCtx() context.Context {
	return context.Background()
}

func newScheme() *runtime.Scheme {
	defer GinkgoRecover()

	scheme := runtime.NewScheme()
	Expect(apiextensionsv1.AddToScheme(scheme)).Should(Succeed())
	Expect(clientgoscheme.AddToScheme(scheme)).Should(Succeed())
	Expect(firewall.AddToScheme(scheme)).Should(Succeed())
	Expect(pg.AddToScheme(scheme)).Should(Succeed())
	Expect(zalando.AddToScheme(scheme)).Should(Succeed())
	Expect(coreosv1.AddToScheme(scheme)).Should(Succeed())

	// +kubebuilder:scaffold:scheme

	return scheme
}

func startMgr(mgr manager.Manager) {
	defer GinkgoRecover()
	Expect(mgr.Start(newCtx())).Should(Succeed())
}

func startTestEnv(env *envtest.Environment) *rest.Config {
	defer GinkgoRecover()

	cfg, err := env.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	return cfg
}
