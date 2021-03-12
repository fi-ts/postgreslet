/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package controllers

import (
	"time"

	pg "github.com/fi-ts/postgreslet/api/v1"
	firewall "github.com/metal-stack/firewall-controller/api/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	zalando "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	core "k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("postgres controller", func() {
	const (
		// duration = time.Second * 10
		interval = time.Second * 2
		timeout  = time.Second * 30
	)

	BeforeEach(func() {})
	AfterEach(func() {})

	Context("complete postgres instance created", func() {
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
					Name:      instance.ToSvcLBName(),
				}, &corev1.Service{}) == nil
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
					Name:      instance.ToSvcLBName(),
				}, &corev1.Service{}) == nil
			}, timeout, interval).ShouldNot(BeTrue())
		})

		It("should delete zalando postgresql in service-cluster", func() {
			z := &zalando.Postgresql{}
			Eventually(func() bool {
				return svcClusterClient.Get(newCtx(), instance.ToPeripheralResourceLookupKey(), z) == nil
			}, timeout, interval).ShouldNot(BeTrue())
		})
	})
})
