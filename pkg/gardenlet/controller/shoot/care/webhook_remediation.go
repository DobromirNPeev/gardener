// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package care

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	webhookmatchers "github.com/gardener/gardener/pkg/gardenlet/operation/botanist/matchers"
	"github.com/gardener/gardener/pkg/utils"
	"github.com/gardener/gardener/pkg/utils/flow"
)

// WebhookRemediation contains required information for shoot webhook remediation.
type WebhookRemediation struct {
	log                    logr.Logger
	initializeShootClients ShootClientInit
	shoot                  *gardencorev1beta1.Shoot
}

// NewWebhookRemediation creates a new instance for webhook remediation.
func NewWebhookRemediation(log logr.Logger, shoot *gardencorev1beta1.Shoot, shootClientInit ShootClientInit) *WebhookRemediation {
	return &WebhookRemediation{
		log:                    log,
		initializeShootClients: shootClientInit,
		shoot:                  shoot,
	}
}

// Remediate mutates shoot webhooks not following the best practices documented by Kubernetes.
func (r *WebhookRemediation) Remediate(ctx context.Context) error {
	shootClient, apiServerRunning, err := r.initializeShootClients()
	if err != nil {
		return err
	}
	if !apiServerRunning {
		return nil
	}

	var (
		fns []flow.TaskFn

		notExcluded          = utils.MustNewRequirement(v1beta1constants.LabelExcludeWebhookFromRemediation, selection.NotIn, "true")
		notManagedByGardener = utils.MustNewRequirement(resourcesv1alpha1.ManagedBy, selection.NotIn, resourcesv1alpha1.GardenerManager)
		labelSelector        = client.MatchingLabelsSelector{Selector: labels.NewSelector().Add(notExcluded).Add(notManagedByGardener)}
	)

	validatingWebhookConfigs := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	if err := shootClient.Client().List(ctx, validatingWebhookConfigs, labelSelector); err != nil {
		return fmt.Errorf("could not get ValidatingWebhookConfigurations of Shoot cluster to remediate problematic webhooks: %w", err)
	}

	for _, config := range validatingWebhookConfigs.Items {
		var (
			webhookConfig = config.DeepCopy()
			mustPatch     bool
			patch         = client.StrategicMergeFrom(webhookConfig.DeepCopy())
			remediations  []string
			matchers      []webhookmatchers.WebhookConstraintMatcher
		)

		for i, w := range webhookConfig.Webhooks {
			remediate := newRemediator(r.log, "ValidatingWebhookConfiguration", webhookConfig.Name, w.Name, &remediations)

			if mustRemediateTimeoutSecondsIfLeaseResource(w.Rules, w.ObjectSelector, w.NamespaceSelector, w.TimeoutSeconds) {
				mustPatch = true
				webhookConfig.Webhooks[i].TimeoutSeconds = remediate.timeoutSecondsToThree()
			}

			if mustRemediateTimeoutSeconds(w.TimeoutSeconds) {
				mustPatch = true
				webhookConfig.Webhooks[i].TimeoutSeconds = remediate.timeoutSeconds()
			}

			if w.FailurePolicy != nil && *w.FailurePolicy == admissionregistrationv1.Ignore {
				continue
			}

			matchers = getMatchingRules(w.Rules, w.ObjectSelector, w.NamespaceSelector)

			if mustRemediateFailurePolicy(matchers) {
				mustPatch = true
				webhookConfig.Webhooks[i].FailurePolicy = remediate.failurePolicy()
			}

			if mustRemediateSelectors(matchers) {
				mustPatch = true
				objectSelector, namespaceSelector := remediate.selectors(matchers)
				webhookConfig.Webhooks[i].ObjectSelector = extendSelector(webhookConfig.Webhooks[i].ObjectSelector, objectSelector...)
				webhookConfig.Webhooks[i].NamespaceSelector = extendSelector(webhookConfig.Webhooks[i].NamespaceSelector, namespaceSelector...)
			}

			if mustRemediateMatchConditions(matchers) {
				mustPatch = true
				webhookConfig.Webhooks[i].MatchConditions = extendMatchConditions(
					webhookConfig.Webhooks[i].MatchConditions,
					remediate.matchConditions(matchers)...,
				)
			}
		}

		if mustPatch {
			fns = append(fns, newPatchFunc(shootClient.Client(), webhookConfig, patch, remediations))
		}
	}

	mutatingWebhookConfigs := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	if err := shootClient.Client().List(ctx, mutatingWebhookConfigs, labelSelector); err != nil {
		return fmt.Errorf("could not get MutatingWebhookConfigurations of Shoot cluster to remediate problematic webhooks: %w", err)
	}

	for _, config := range mutatingWebhookConfigs.Items {
		var (
			webhookConfig = config.DeepCopy()
			mustPatch     bool
			patch         = client.StrategicMergeFrom(webhookConfig.DeepCopy())
			remediations  []string
			matchers      []webhookmatchers.WebhookConstraintMatcher
		)

		for i, w := range webhookConfig.Webhooks {
			remediate := newRemediator(r.log, "MutatingWebhookConfiguration", webhookConfig.Name, w.Name, &remediations)

			if mustRemediateTimeoutSecondsIfLeaseResource(w.Rules, w.ObjectSelector, w.NamespaceSelector, w.TimeoutSeconds) {
				mustPatch = true
				webhookConfig.Webhooks[i].TimeoutSeconds = remediate.timeoutSecondsToThree()
			}

			if mustRemediateTimeoutSeconds(w.TimeoutSeconds) {
				mustPatch = true
				webhookConfig.Webhooks[i].TimeoutSeconds = remediate.timeoutSeconds()
			}

			if w.FailurePolicy != nil && *w.FailurePolicy == admissionregistrationv1.Ignore {
				continue
			}

			matchers = getMatchingRules(w.Rules, w.ObjectSelector, w.NamespaceSelector)

			if mustRemediateFailurePolicy(matchers) {
				mustPatch = true
				webhookConfig.Webhooks[i].FailurePolicy = remediate.failurePolicy()
			}

			if mustRemediateSelectors(matchers) {
				mustPatch = true
				objectSelector, namespaceSelector := remediate.selectors(matchers)
				webhookConfig.Webhooks[i].ObjectSelector = extendSelector(webhookConfig.Webhooks[i].ObjectSelector, objectSelector...)
				webhookConfig.Webhooks[i].NamespaceSelector = extendSelector(webhookConfig.Webhooks[i].NamespaceSelector, namespaceSelector...)
			}

			if mustRemediateMatchConditions(matchers) {
				mustPatch = true
				webhookConfig.Webhooks[i].MatchConditions = extendMatchConditions(
					webhookConfig.Webhooks[i].MatchConditions,
					remediate.matchConditions(matchers)...,
				)
			}
		}

		if mustPatch {
			fns = append(fns, newPatchFunc(shootClient.Client(), webhookConfig, patch, remediations))
		}
	}

	return flow.Parallel(fns...)(ctx)
}

func getMatchingRules(
	rules []admissionregistrationv1.RuleWithOperations,
	objectSelector, namespaceSelector *metav1.LabelSelector,
) []webhookmatchers.WebhookConstraintMatcher {
	var matchers []webhookmatchers.WebhookConstraintMatcher
	for _, rule := range rules {
		for _, matcher := range webhookmatchers.WebhookConstraintMatchers {
			if matcher.Match(rule, objectSelector, namespaceSelector) {
				matchers = append(matchers, matcher)
			}
		}
	}
	return matchers
}

func mustRemediateTimeoutSecondsIfLeaseResource(rules []admissionregistrationv1.RuleWithOperations,
	objLabelSelector *metav1.LabelSelector,
	namespaceLabelSelector *metav1.LabelSelector,
	timeoutSeconds *int32) bool {
	for _, rule := range rules {
		for _, matcher := range webhookmatchers.WebhookConstraintMatchersForLeases {
			if matcher.Match(rule, objLabelSelector, namespaceLabelSelector) &&
				(timeoutSeconds == nil || *timeoutSeconds > WebhookMaximumTimeoutSecondsNotProblematicForLeases) {
				return true
			}
		}
	}

	return false
}

func mustRemediateTimeoutSeconds(timeoutSeconds *int32) bool {
	return timeoutSeconds == nil || *timeoutSeconds > WebhookMaximumTimeoutSecondsNotProblematic
}

func mustRemediateSelectors(matchers []webhookmatchers.WebhookConstraintMatcher) bool {
	for _, matcher := range matchers {
		if len(matcher.ObjectLabels) > 0 || len(matcher.NamespaceLabels) > 0 {
			return true
		}
	}
	return false
}

func mustRemediateFailurePolicy(matchers []webhookmatchers.WebhookConstraintMatcher) bool {
	for _, matcher := range matchers {
		if len(matcher.ObjectNames) > 0 {
			continue // name-scoped matchers are remediated via CEL matchConditions, not failurePolicy
		}
		if matcher.NamespaceLabels == nil && matcher.ObjectLabels == nil {
			return true
		}
	}
	return false
}

func mustRemediateMatchConditions(matchers []webhookmatchers.WebhookConstraintMatcher) bool {
	for _, matcher := range matchers {
		if len(matcher.ObjectNames) > 0 {
			return true
		}
	}
	return false
}

type remediator struct {
	log               logr.Logger
	webhookConfigKind string
	webhookConfigName string
	webhookName       string
	remediations      *[]string
}

func newRemediator(log logr.Logger, webhookConfigKind, webhookConfigName, webhookName string, remediations *[]string) remediator {
	return remediator{
		log: log.WithValues(
			"kind", webhookConfigKind,
			"webhookConfigName", webhookConfigName,
			"webhookName", webhookName,
		),
		webhookConfigKind: webhookConfigKind,
		webhookConfigName: webhookConfigName,
		webhookName:       webhookName,
		remediations:      remediations,
	}
}

func (r *remediator) timeoutSeconds() *int32 {
	var timeoutSeconds int32 = WebhookMaximumTimeoutSecondsNotProblematic
	r.reportf("timeoutSeconds", "set to %d", timeoutSeconds)
	return new(timeoutSeconds)
}

func (r *remediator) timeoutSecondsToThree() *int32 {
	var timeoutSeconds int32 = WebhookMaximumTimeoutSecondsNotProblematicForLeases
	r.reportf("timeoutSeconds", "set to %d", timeoutSeconds)
	return new(timeoutSeconds)
}

func (r *remediator) selectors(matchers []webhookmatchers.WebhookConstraintMatcher) (objectSelector, namespaceSelector []metav1.LabelSelectorRequirement) {
	for _, matcher := range matchers {
		for k, v := range matcher.ObjectLabels {
			objectSelector = append(objectSelector, newNotInLabelSelectorRequirement(k, v))
		}
		for k, v := range matcher.NamespaceLabels {
			namespaceSelector = append(namespaceSelector, newNotInLabelSelectorRequirement(k, v))
		}
	}

	objectSelector = removeDuplicateRequirements(objectSelector)
	namespaceSelector = removeDuplicateRequirements(namespaceSelector)

	if len(objectSelector) > 0 {
		r.reportf("objectSelector", "extended with %s", objectSelector)
	}
	if len(namespaceSelector) > 0 {
		r.reportf("namespaceSelector", "extended with %s", namespaceSelector)
	}

	return
}

func (r *remediator) matchConditions(matchers []webhookmatchers.WebhookConstraintMatcher) []admissionregistrationv1.MatchCondition {
	var conditions []admissionregistrationv1.MatchCondition
	seen := sets.New[string]()
	for _, matcher := range matchers {
		for _, name := range matcher.ObjectNames {
			mc := newExcludeObjectNameMatchCondition(matcher.GVR, name)
			if seen.Has(mc.Name) {
				continue
			}
			conditions = append(conditions, mc)
			seen.Insert(mc.Name)
			r.reportf("matchConditions", "extended with %q to skip object %q", mc.Name, name)
		}
	}
	return conditions
}

func (r *remediator) failurePolicy() *admissionregistrationv1.FailurePolicyType {
	ignore := admissionregistrationv1.Ignore
	r.reportf("failurePolicy", "set to %s", ignore)
	return &ignore
}

func (r *remediator) reportf(fieldName string, messageFmt string, args ...any) {
	r.log.Info("Remediating", "fieldName", fieldName)
	*r.remediations = append(*r.remediations, fmt.Sprintf("%s of webhook %q was %s", fieldName, r.webhookName, fmt.Sprintf(messageFmt, args...)))
}

func newPatchFunc(shootClient client.Client, webhookConfig client.Object, patch client.Patch, remediations []string) func(context.Context) error {
	annotations := webhookConfig.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string, 1)
	}

	annotations[v1beta1constants.GardenerWarning] = "ATTENTION: This webhook configuration has been modified by " +
		"Gardener since it does not follow the best practices recommended by Kubernetes " +
		"(https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#best-practices-and-warnings) " +
		"and might interfere with the cluster operations. Please make sure to follow these recommendations to prevent " +
		"future interventions. When you are done, please remove this annotation. See also " +
		"https://github.com/gardener/gardener/blob/master/docs/usage/shoot/shoot_status.md#constraints for further information.\n" +
		"The following modifications have been made:\n" +
		strings.Join(addHyphenPrefix(remediations), "\n")
	webhookConfig.SetAnnotations(annotations)

	return func(ctx context.Context) error {
		return shootClient.Patch(ctx, webhookConfig, patch)
	}
}

func addHyphenPrefix(list []string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, "- "+v)
	}
	return out
}

func newNotInLabelSelectorRequirement(key, value string) metav1.LabelSelectorRequirement {
	return metav1.LabelSelectorRequirement{
		Key:      key,
		Operator: metav1.LabelSelectorOpNotIn,
		Values:   []string{value},
	}
}

func removeDuplicateRequirements(requirements []metav1.LabelSelectorRequirement) []metav1.LabelSelectorRequirement {
	var (
		keyValues   = sets.New[string]()
		keyValuesID = func(requirement metav1.LabelSelectorRequirement) string {
			return requirement.Key + strings.Join(requirement.Values, "")
		}

		out []metav1.LabelSelectorRequirement
	)

	for _, requirement := range requirements {
		id := keyValuesID(requirement)

		if keyValues.Has(id) {
			continue
		}

		out = append(out, requirement)
		keyValues.Insert(id)
	}

	return out
}

func extendSelector(selector *metav1.LabelSelector, requirements ...metav1.LabelSelectorRequirement) *metav1.LabelSelector {
	if selector == nil {
		selector = &metav1.LabelSelector{}
	}

	selector.MatchExpressions = append(selector.MatchExpressions, requirements...)
	return selector
}

func newExcludeObjectNameMatchCondition(gvr schema.GroupVersionResource, name string) admissionregistrationv1.MatchCondition {
	return admissionregistrationv1.MatchCondition{
		Name: "gardener-exclude-" + name,
		Expression: fmt.Sprintf(
			`!(request.resource.group == %q && request.resource.resource == %q && object != null && has(object.metadata.name) && object.metadata.name == %q)`,
			gvr.Group, gvr.Resource, name,
		),
	}
}

func extendMatchConditions(existing []admissionregistrationv1.MatchCondition, additional ...admissionregistrationv1.MatchCondition) []admissionregistrationv1.MatchCondition {
	present := sets.New[string]()
	for _, mc := range existing {
		present.Insert(mc.Name)
	}
	for _, mc := range additional {
		if present.Has(mc.Name) {
			continue // IDEMPOTENT: do not duplicate on re-run of the care loop
		}
		existing = append(existing, mc)
		present.Insert(mc.Name)
	}
	return existing
}
