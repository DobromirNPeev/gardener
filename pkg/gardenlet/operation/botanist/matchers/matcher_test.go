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

var _ = Describe("Matcher", func() {
	Describe("#Match", func() {
		var (
			ruleForResource = func(resource string) admissionregistrationv1.RuleWithOperations {
				return admissionregistrationv1.RuleWithOperations{
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{""},
						APIVersions: []string{"v1"},
						Resources:   []string{resource},
					},
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				}
			}

			configMapsMatcher = WebhookConstraintMatcher{
				GVR:         corev1.SchemeGroupVersion.WithResource("configmaps"),
				ObjectNames: []string{"kube-root-ca.crt"},
			}
		)

		It("should match a configmaps CREATE rule (name-blind detection)", func() {
			// ObjectNames does NOT participate in Match(); detection stays at GVR/scope granularity.
			Expect(configMapsMatcher.Match(ruleForResource("configmaps"), nil, nil)).To(BeTrue())
		})

		It("should NOT match a secrets rule even though ObjectNames is set", func() {
			Expect(configMapsMatcher.Match(ruleForResource("secrets"), nil, nil)).To(BeFalse())
		})

		It("should match configmaps in all namespaces/objects (ObjectNames does not narrow Match)", func() {
			// nil ObjectLabels/NamespaceLabels => matchAllObjects && matchAllNamespaces: any object
			// selector and any namespace selector still match, i.e. ObjectNames does not disturb the
			// namespace/object matching semantics.
			objectSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"foo": "bar"}}
			namespaceSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"baz": "qux"}}

			Expect(configMapsMatcher.Match(ruleForResource("configmaps"), objectSelector, namespaceSelector)).To(BeTrue())
			Expect(configMapsMatcher.Match(ruleForResource("configmaps"), nil, namespaceSelector)).To(BeTrue())
			Expect(configMapsMatcher.Match(ruleForResource("configmaps"), objectSelector, nil)).To(BeTrue())
		})
	})
})
