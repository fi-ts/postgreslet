/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	pg "github.com/fi-ts/postgreslet/api/v1"
	firewall "github.com/metal-stack/firewall-controller/v2/api/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("postgres controller", func() {
	BeforeEach(func() {})
	AfterEach(func() {})

	Context("complete postgres instance created", func() {
		// Todo: Consider shifting the creation logic to here.
		// The instance is created in `suite_test.go`.
		It("should add finalizer to the instance", func() {
			Eventually(func() bool {
				if err := ctrlClusterClient.Get(newCtx(), *instance.ToKey(), instance); err != nil {
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
				return svcClusterClient.Get(newCtx(), types.NamespacedName{
					Name: instance.ToPeripheralResourceNamespace(),
				}, &core.Namespace{}) == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should create zalando postgresql in service-cluster", func() {
			z := &zalando.Postgresql{}
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), instance.ToPeripheralResourceLookupKey(), z) == nil
			}, timeout, interval).Should(BeTrue())

			// Todo: Check details of z
		})

		It("should create service of type load-balancer in service-cluster", func() {
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), types.NamespacedName{
					Namespace: instance.ToPeripheralResourceNamespace(),
					Name:      instance.ToSharedSvcLBName(),
				}, &core.Service{}) == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should create crd clusterwidenetworkpolicy in service-cluster", func() {
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), types.NamespacedName{
					Namespace: firewall.ClusterwideNetworkPolicyNamespace,
					Name:      instance.ToPeripheralResourceName(),
				}, &firewall.ClusterwideNetworkPolicy{}) == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should create user-passwords-secret in control-plane-cluster", func() {
			Eventually(func() bool {
				return ctrlClusterClient.Get(newCtx(), types.NamespacedName{
					Namespace: instance.Namespace,
					Name:      instance.ToUserPasswordsSecretName(),
				}, &core.Secret{}) == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("postgres instance being deleted", func() {
		It("should delete crd clusterwidenetworkpolicy in service-cluster", func() {
			Expect(ctrlClusterClient.Delete(newCtx(), instance)).Should(Succeed())
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), types.NamespacedName{
					Namespace: firewall.ClusterwideNetworkPolicyNamespace,
					Name:      instance.ToPeripheralResourceName(),
				}, &firewall.ClusterwideNetworkPolicy{}) == nil
			}, timeout, interval).ShouldNot(BeTrue())
		})

		It("should delete service of type load-balancer in service-cluster", func() {
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), types.NamespacedName{
					Namespace: instance.ToPeripheralResourceNamespace(),
					Name:      instance.ToSharedSvcLBName(),
				}, &core.Service{}) == nil
			}, timeout, interval).ShouldNot(BeTrue())
		})

		It("should delete zalando postgresql in service-cluster", func() {
			z := &zalando.Postgresql{}
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), instance.ToPeripheralResourceLookupKey(), z) == nil
			}, timeout, interval).ShouldNot(BeTrue())
		})

		It("should delete user-passwords-secret in control-plane-cluster", func() {
			Eventually(func() bool {
				return ctrlClusterClient.Get(newCtx(), types.NamespacedName{
					Namespace: instance.Namespace,
					Name:      instance.ToUserPasswordsSecretName(),
				}, &core.Secret{}) == nil
			}, timeout, interval).ShouldNot(BeTrue())
		})
	})
})
