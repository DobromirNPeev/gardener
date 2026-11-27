// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package matchers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/gardener/gardener/pkg/gardenlet/operation/botanist/matchers"
)

var _ = Describe("WebhookConstraintMatchers", func() {
	// anyMatches reports whether any configured WebhookConstraintMatcher matches the given configmaps rule and selectors.
	anyMatches := func(rule admissionregistrationv1.RuleWithOperations, objSelector, nsSelector *metav1.LabelSelector) bool {
		for _, m := range WebhookConstraintMatchers {
			if m.Match(rule, objSelector, nsSelector) {
				return true
			}
		}
		return false
	}

	configmapsRule := func(ops ...admissionregistrationv1.OperationType) admissionregistrationv1.RuleWithOperations {
		return admissionregistrationv1.RuleWithOperations{
			Rule: admissionregistrationv1.Rule{
				APIGroups:   []string{corev1.GroupName},
				APIVersions: []string{"v1"},
				Resources:   []string{"configmaps"},
			},
			Operations: ops,
		}
	}

	Describe("configmaps detection for the kube-root-ca.crt incident", func() {
		DescribeTable("a configmaps webhook matches a constraint matcher regardless of namespace/object scoping",
			func(objSelector, nsSelector *metav1.LabelSelector, ops ...admissionregistrationv1.OperationType) {
				Expect(anyMatches(configmapsRule(ops...), objSelector, nsSelector)).To(BeTrue())
			},

			Entry("CREATE, no selectors", nil, (*metav1.LabelSelector)(nil), admissionregistrationv1.Create),
			Entry("UPDATE, no selectors", nil, (*metav1.LabelSelector)(nil), admissionregistrationv1.Update),
			Entry("CREATE, namespaceSelector scoped to a customer namespace",
				nil,
				&metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "customer-ns"}},
				admissionregistrationv1.Create,
			),
			Entry("UPDATE, namespaceSelector excluding kube-system",
				nil,
				&metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"kube-system"}},
				}},
				admissionregistrationv1.Update,
			),
			Entry("CREATE, objectSelector scoped to a customer app",
				&metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "customer-app"}},
				(*metav1.LabelSelector)(nil),
				admissionregistrationv1.Create,
			),
		)

		It("does not match a DELETE-only configmaps webhook", func() {
			Expect(anyMatches(configmapsRule(admissionregistrationv1.Delete), nil, nil)).To(BeFalse())
		})
	})
})
