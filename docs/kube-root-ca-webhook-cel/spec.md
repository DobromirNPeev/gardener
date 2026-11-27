# Spec: Webhook remediation via CEL `matchConditions` for `kube-root-ca.crt`

## Goal
During Shoot CA/credentials rotation, `WaitUntilKubeRootCAConfigMapsUpdated`
(`pkg/gardenlet/operation/botanist/kubecontrollermanager.go:94`) walks every `kube-root-ca.crt`
ConfigMap in ALL namespaces, waits until it contains the new CA bundle, then PATCHes a
confirmation label. A customer admission webhook with `failurePolicy: Fail` that intercepts
`configmaps` CREATE/UPDATE and has no reachable backing pods blocks those writes and spins the
loop forever (`RetryUntilTimeout(30s,5m)`).

Approach A extends the existing care-loop webhook remediation so that when it detects a customer
webhook on `configmaps`, it appends a null-safe CEL `matchCondition` that makes the webhook SKIP
exactly the object named `kube-root-ca.crt` (in every namespace), leaving all other ConfigMaps
guarded and `failurePolicy` UNCHANGED. This is a NEW, THIRD remediation path, strictly additive to
the existing failurePolicy and selectors paths.

## Context verified by the architect
- The `gardener-reviews` worktree has NO `CLAUDE.md`; deps are module-mode (not vendored).
- `admissionregistrationv1.MatchCondition` / `MatchConditions` are already used in-tree
  (`pkg/component/gardener/admissioncontroller/webhooks.go:308,452,541`) — API available, no new deps.
- Gardener already uses null-safe CEL with `object != null` / `has(...)` guards — follow that convention.

## Affected files
- `pkg/gardenlet/operation/botanist/matchers/matcher.go` — new field `ObjectNames []string` on
  `WebhookConstraintMatcher`; new `configmaps` entry with `ObjectNames: []string{"kube-root-ca.crt"}`;
  `Match()` stays name-blind.
- `pkg/gardenlet/controller/shoot/care/webhook_remediation.go` — new predicate
  `mustRemediateMatchConditions`, new method `remediate.matchConditions`, new helper
  `extendMatchConditions`; wired into BOTH loops (Validating ~76-106, Mutating ~127-157). Plus a
  guarding change to `mustRemediateFailurePolicy` and a tightening of `mustRemediateSelectors`.
- `pkg/gardenlet/controller/shoot/care/webhook_remediation_test.go` — new It/table tests mirroring
  the `objectSelector`/`failurePolicy` cases.
- `pkg/gardenlet/operation/botanist/matchers/matcher_test.go` — NEW file (package has no test file
  today) OR put the `Match` test in `webhook_remediation_test.go`.
- `pkg/gardenlet/controller/shoot/care/constraints_test.go` — test documenting the `IsProblematicWebhook`
  consequence.

## Approach

### 1. New field `ObjectNames` on `WebhookConstraintMatcher`
Add to the struct (around matcher.go:188-194):
```go
// ObjectNames, when non-empty, signals that remediation for this matcher must be done via
// name-scoped CEL matchConditions (skipping exactly these object names) instead of failurePolicy
// or objectSelector. It does NOT participate in Match() because admission webhook rules are
// name-blind (no resourceNames); detection stays at GVR/scope granularity.
ObjectNames []string
```
`Match()` is UNCHANGED. Key semantics: an entry with `ObjectNames` has `ObjectLabels == nil` and
`NamespaceLabels == nil`, so `matchAllObjects == true` and `matchAllNamespaces == true` → detection
surface equals a broad `configmaps` entry (all namespaces, all names). This is INTENDED: detection
is coarse (the webhook really does intercept `configmaps`), but the REMEDIATION is name-scoped via
CEL. Entries WITHOUT `ObjectNames` (nil) are entirely unaffected — the field is additive and read
only by the new path.

### 2. New matcher entry
Add a NEW entry near matcher.go:67; DO NOT touch or weaken the existing kube-system configmaps entry:
```go
// kube-root-ca.crt ConfigMap exists in EVERY namespace, has no stable label and is written during
// CA/credentials rotation. A blocking customer webhook on configmaps hangs rotation. Remediate via a
// name-scoped CEL matchCondition (see webhook_remediation.go) instead of failurePolicy/objectSelector.
{GVR: corev1.SchemeGroupVersion.WithResource("configmaps"), ObjectNames: []string{"kube-root-ca.crt"}},
```

### 3. New remediation path in `webhook_remediation.go`

Predicate (near mustRemediateSelectors/mustRemediateFailurePolicy, ~202-213):
```go
func mustRemediateMatchConditions(matchers []webhookmatchers.WebhookConstraintMatcher) bool {
	for _, matcher := range matchers {
		if len(matcher.ObjectNames) > 0 {
			return true
		}
	}
	return false
}
```

Method on `remediator` (near selectors, ~249):
```go
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
```

CEL condition helper:
```go
func newExcludeObjectNameMatchCondition(gvr schema.GroupVersionResource, name string) admissionregistrationv1.MatchCondition {
	return admissionregistrationv1.MatchCondition{
		Name: "gardener-exclude-" + name,
		Expression: fmt.Sprintf(
			`!(request.resource.group == %q && request.resource.resource == %q && object != null && has(object.metadata.name) && object.metadata.name == %q)`,
			gvr.Group, gvr.Resource, name,
		),
	}
}
```

Idempotent helper (mirror extendSelector ~344):
```go
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
```

Wiring (in BOTH loops, after the mustRemediateSelectors block — ~100-105 and ~151-156):
```go
if mustRemediateMatchConditions(matchers) {
	mustPatch = true
	webhookConfig.Webhooks[i].MatchConditions = extendMatchConditions(
		webhookConfig.Webhooks[i].MatchConditions,
		remediate.matchConditions(matchers)...,
	)
}
```
New imports: `schema "k8s.io/apimachinery/pkg/runtime/schema"` and `fmt` (already present); `sets`
already imported (line 17).

### 4. Exact CEL expression, name, and predicate partitioning

**MatchCondition.Name.** Must be a valid qualified name. Fixed deterministic name
`"gardener-exclude-kube-root-ca.crt"` (32 chars, ≤63 OK). Determinism is CRITICAL for idempotency
(`extendMatchConditions` compares by `Name`).

**Expression (null-safe under `failurePolicy: Fail`).** Any CEL eval error → request rejected →
hang recreated. Therefore:
```
!(request.resource.group == "" && request.resource.resource == "configmaps" && object != null && has(object.metadata.name) && object.metadata.name == "kube-root-ca.crt")
```
- `request.resource` always populated by the apiserver for an admission review → safe.
- `object` can be `null` on DELETE (present on CREATE/UPDATE) → `object != null &&` before access.
  The matcher's rule only catches CREATE/UPDATE (`ruleMatcher.operation()`), but the webhook's own
  rule may include DELETE, so the guard is defensive.
- `has(object.metadata.name)` before accessing `name`; `kube-root-ca.crt` doesn't use `generateName`
  so name is always present on CREATE/UPDATE. Same convention as
  `pkg/component/gardener/admissioncontroller/webhooks.go:311`.
- Returns `false` ONLY for the exact match (core group `""`, resource `configmaps`, name
  `kube-root-ca.crt`) → the webhook is skipped only for that object; everything else → `true` → the
  webhook runs unchanged.

**matchConditions are evaluated BEFORE the webhook is called** (k8s admission chain), including for
MUTATING webhooks. A skipped webhook is never invoked and never mutates `kube-root-ca.crt` — closes
the "mutating webhook that changes kube-root-ca.crt" edge case.

**Predicate partitioning for the new entry (`ObjectNames` set, labels nil):**
- `mustRemediateFailurePolicy` (~206): currently returns `true` when both label-sets are nil. For the
  new entry both ARE nil, so it WOULD flip failurePolicy. MUST change so an entry with `ObjectNames`
  does NOT trigger failurePolicy:
  ```go
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
  ```
  This is the ONLY change to an existing predicate; strictly necessary to avoid recreating the
  "disarm all configmaps" behavior. Behavior for all entries without `ObjectNames` is identical.
- `mustRemediateSelectors` (~202): currently `len(matchers) > 0`, so it WOULD be `true` for the new
  entry. `selectors()` iterates `ObjectLabels`/`NamespaceLabels` which are nil → produces no
  requirement → `extendSelector` called with empty slice → selectors functionally unchanged, but may
  create a non-nil empty `&metav1.LabelSelector{}` if a selector already existed. DECISION (made by
  orchestrator): TIGHTEN `mustRemediateSelectors` to require non-empty labels:
  ```go
  func mustRemediateSelectors(matchers []webhookmatchers.WebhookConstraintMatcher) bool {
  	for _, matcher := range matchers {
  		if len(matcher.ObjectLabels) > 0 || len(matcher.NamespaceLabels) > 0 {
  			return true
  		}
  	}
  	return false
  }
  ```
  Safe: existing failurePolicy tests (e.g. label-less `{GVR: foobars}`) only assert
  `FailurePolicy == Ignore`, not that selectors path ran. Verify existing selector tests
  (labeled entries) still pass.

### 5. Warning annotation / reportf
The new path calls `r.reportf("matchConditions", ...)` per added condition, so the human-readable
remediation line appears in the `gardener.cloud/warning` annotation via the same `newPatchFunc`
(~283). Example: `matchConditions of webhook "some-webhook" was extended with
"gardener-exclude-kube-root-ca.crt" to skip object "kube-root-ca.crt"`.

## Edge cases
- `object == null` on DELETE: guarded; expression returns `true` (does not exclude) → safe.
- CEL eval error under `failurePolicy: Fail`: avoided via null guards and built-in fields only; no
  external functions.
- Idempotency: re-run must not duplicate the MatchCondition — guaranteed by `extendMatchConditions`
  (compare by `Name`) + deterministic name. Test with two consecutive `Remediate`.
- `extendSelector` making a non-nil empty selector: eliminated by tightening `mustRemediateSelectors`.
- Version skew: `matchConditions` GA since k8s 1.30; min Shoot version 1.33 → NO version gate.
- Mutating webhook mutating kube-root-ca.crt: matchConditions evaluated before invocation → skipped
  webhook never mutates. No residual hang.
- Multiple matching rules / configmaps entries: `matchConditions()` dedupes by `Name` internally +
  `extendMatchConditions` dedupes against existing.

## Boundaries / risks
- `constraints.go` / `IsProblematicWebhook` uses the SAME `matchers.WebhookConstraintMatchers`.
  Adding the new configmaps entry means `IsProblematicWebhook` will now rate ANY configmaps
  CREATE/UPDATE webhook with `failurePolicy: Fail/nil` and timeout > 15s as "problematic" → sets
  `HibernationPossible`/`MaintenancePreconditionsSatisfied` to `False` with `ErrorProblematicWebhook`.
  This EXTENDS behavior (previously only kube-system configmaps). DECISION (made by orchestrator):
  KEEP the effect — such a webhook genuinely IS problematic (it caused the incident) and the customer
  should be warned. Add a `constraints_test.go` case documenting the new behavior so it is deliberate.
- No changes to stored API types (`pkg/apis/**`), no generated code, no protobuf — pure
  controller/matcher logic. `ObjectNames` is a field on an internal (non-API) struct — no
  conversion/defaults/validation needed.

## What must NOT change
- Existing entries in `WebhookConstraintMatchers` and `WebhookConstraintMatchersForLeases` (incl. the
  kube-system configmaps entry at line 67) — verbatim.
- `Match()` logic in `matcher.go` and all of `rule_matcher.go`.
- failurePolicy path (`remediate.failurePolicy`, ~272) behavior for all entries without `ObjectNames`
  — only the `continue` guard is added to `mustRemediateFailurePolicy`.
- selectors path (`remediate.selectors`, `newNotInLabelSelectorRequirement`, `extendSelector`,
  `removeDuplicateRequirements`) — unaffected (the `mustRemediateSelectors` tightening does not change
  behavior for labeled entries).
- lease path and timeout path — fully unaffected.
- `newPatchFunc`/warning-annotation mechanism — used as-is.

## Test plan

### Unit (`webhook_remediation_test.go`), mirroring existing cases, for Validating AND Mutating:
1. "matchConditions for configmaps" — webhook with `FailurePolicy: &fail`, rule
   `{APIGroups:[""], APIVersions:["v1"], Resources:["configmaps"], Operations:[Create,Update]}`.
   Override `matchers.WebhookConstraintMatchers` via `test.WithVar` to
   `[]{{GVR: corev1...WithResource("configmaps"), ObjectNames: []string{"kube-root-ca.crt"}}}`. Assert:
   - `Annotations` contains `gardener.cloud/warning`.
   - `Webhooks[0].MatchConditions` has exactly one condition `Name == "gardener-exclude-kube-root-ca.crt"`
     with the expected Expression.
   - `Webhooks[0].FailurePolicy` stays `&fail` (NOT flipped).
   - `Webhooks[0].ObjectSelector` and `NamespaceSelector` stay `nil` (untouched).
2. Idempotency — call `Remediate` twice; `MatchConditions` still has exactly one condition.
3. Mutating mirror of (1).
4. Regression — existing failurePolicy/objectSelector/namespaceSelector/both/timeout tests pass
   unchanged.

### Unit for `Match()` (`matcher_test.go` new file + suite, or inline):
- `{GVR: configmaps, ObjectNames: []string{"kube-root-ca.crt"}}.Match(rule(configmaps CREATE), nil, nil)` → true.
- same matcher vs `secrets` rule → false.
- `ObjectNames` does not affect namespace/object selector logic.

### Unit for constraint (`constraints_test.go`):
- `IsProblematicWebhook(&fail, nil, nil, rules(configmaps CREATE), nil)` → true (documents the
  deliberately extended behavior).

### Commands (module-scoped, no CLAUDE.md in worktree):
- `go test ./pkg/gardenlet/controller/shoot/care/...`
- `go test ./pkg/gardenlet/operation/botanist/matchers/...`
- `go vet ./pkg/gardenlet/...` and `golangci-lint run ./pkg/gardenlet/...` (or `make check`).

### e2e / live verification (with `webhookRemediatorEnabled`):
1. Plant blocking `ValidatingWebhookConfiguration` on `configmaps` CREATE/UPDATE, `failurePolicy: Fail`,
   no reachable backing pods.
2. `rotate-ca-start` on the shoot.
3. Confirm the care loop patches the webhook with CEL `matchCondition` `gardener-exclude-kube-root-ca.crt`
   and the warning annotation.
4. Confirm `WaitUntilKubeRootCAConfigMapsUpdated` stops looping (no more repeating "Found
   kube-root-ca.crt ConfigMaps not yet confirmed...") and rotation advances.
5. Confirm the webhook STILL intercepts CREATE/UPDATE of other ConfigMaps (a differently-named test
   ConfigMap is still blocked) — proving the exclusion is name-only.

## Orchestrator decisions (both open questions resolved)
1. `IsProblematicWebhook` INCLUDES the new entry (keep the constraint/warning effect — the webhook is
   genuinely problematic). Document with a test.
2. `mustRemediateSelectors` is TIGHTENED to require non-empty labels (safe precision; avoids a
   cosmetic non-nil empty selector).
