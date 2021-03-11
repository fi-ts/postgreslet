/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"os"
	"path/filepath"
	"time"

	pg "github.com/fi-ts/postgreslet/api/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var _ = Describe("Postgres controller", func() {
	const (
		timeout = time.Second * 30
		// duration = time.Second * 10
		interval = time.Second * 2
		// interval = time.Second * 250
	)

	BeforeEach(func() {})
	AfterEach(func() {})

	Context("...", func() {
		It("should succeed", func() {
			nsObj := &core.Namespace{}
			nsObj.Name = "postgreslet-system"
			Expect(svcClusterClient.Create(newCxt(), nsObj)).Should(Succeed())
			// Eventually(func() bool {
			// 	return svcClusterClient.Get(newCxt(), types.NamespacedName{
			// 		Name: nsObj.Name,
			// 	}, nsObj) != nil
			// }, timeout, interval).Should(BeTrue())

			bytes, err := os.ReadFile(filepath.Join("..", "test", "cm-sidecar.yaml"))
			Expect(err).ToNot(HaveOccurred())

			cm := &core.ConfigMap{}
			Expect(yaml.Unmarshal(bytes, cm)).Should(Succeed())

			Expect(svcClusterClient.Create(newCxt(), cm)).Should(Succeed())
		})

		It("", func() {
			bytes, err := os.ReadFile(filepath.Join("..", "config", "samples", "postgres.yaml"))
			Expect(err).ToNot(HaveOccurred())
			Expect(yaml.Unmarshal(bytes, instance)).Should(Succeed())

			Expect(ctrlClusterClient.Create(newCxt(), instance)).Should(Succeed())

			// Fetch the instance eventually
			Eventually(func() bool {
				return ctrlClusterClient.Get(newCxt(), *instance.ToKey(), instance) != nil
			}, timeout, interval).Should(BeTrue())
		})

		// It("should install sidercar ConfigMap in service-cluster", func() {
		// 	nsObj := &core.Namespace{}
		// 	nsObj.Name = "postgreslet-system"
		// 	Expect(svcClusterClient.Create(newCxt(), nsObj)).Should(Succeed())

		// 	// Unmarshal sidecar ConfigMap
		// 	bytes, err := os.ReadFile(filepath.Join("..", "test", "cm-sidecar.yaml"))
		// 	Expect(err).ToNot(HaveOccurred())

		// 	cm := &core.ConfigMap{}
		// 	Expect(yaml.Unmarshal(bytes, cm)).Should(Succeed())

		// 	Expect(svcClusterClient.Create(newCxt(), cm)).Should(Succeed())
		// })

		// It("should create credential Secret in service-cluster", func() {
		// 	users := []string{"postgres", "standby"}
		// 	for i := range users {

		// 	}
		// })

		// It("should create Postgres in control-cluster", func() {
		// 	Expect(ctrlClusterClient.Create(newCxt(), instance)).Should(Succeed())

		// 	// Fetch it eventually
		// 	Eventually(func() bool {
		// 		return ctrlClusterClient.Get(newCxt(), *instance.ToKey(), instance) != nil
		// 	}, timeout, interval).Should(BeTrue())
		// })

		It("should add finalizer", func() {
			Eventually(func() bool {
				if err := ctrlClusterClient.Get(newCxt(), *instance.ToKey(), instance); err != nil {
					return false
				}

				if len(instance.Finalizers) == 0 {
					return false
				}
				return instance.Finalizers[0] == pg.PostgresFinalizerName
			}, timeout, interval).Should(BeTrue())
		})

		It("should create peripheral resource namespace in service-cluster", func() {
			Eventually(func() bool {
				lookupKey := types.NamespacedName{
					Name: instance.ToPeripheralResourceNamespace(),
				}
				return svcClusterClient.Get(newCxt(), lookupKey, &core.Namespace{}) != nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should create zalando postgresql in service-cluster", func() {
			z := &zalando.Postgresql{}
			Eventually(func() bool {
				return svcClusterClient.Get(newCxt(), instance.ToPeripheralResourceLookupKey(), z) != nil
			}, timeout, interval).Should(BeTrue())

			// Todo: Check details of z
		})
	})
})

// func createConfigMapSidecar() {
// 	defer GinkgoRecover()

// 	nsObj := &core.Namespace{}
// 	nsObj.Name = "postgreslet-system"
// 	Expect(svcClusterClient.Create(newCxt(), nsObj)).Should(Succeed())
// 	Eventually(func() bool {
// 		return svcClusterClient.Get(newCxt(), types.NamespacedName{
// 			Name: nsObj.Name,
// 		}, nsObj) != nil
// 	}, timeout, interval).Should(BeTrue())

// 	bytes, err := os.ReadFile(filepath.Join("..", "test", "cm-sidecar.yaml"))
// 	Expect(err).ToNot(HaveOccurred())

// 	cm := &core.ConfigMap{}
// 	Expect(yaml.Unmarshal(bytes, cm)).Should(Succeed())

// 	Expect(svcClusterClient.Create(newCxt(), cm)).Should(Succeed())
// }

// func createPostgres() {
// 	defer GinkgoRecover()

// 	Expect(ctrlClusterClient.Create(newCxt(), instance)).Should(Succeed())

// 	// Fetch the instance eventually
// 	Eventually(func() bool {
// 		return ctrlClusterClient.Get(newCxt(), *instance.ToKey(), instance) != nil
// 	}, timeout, interval).Should(BeTrue())
// }
