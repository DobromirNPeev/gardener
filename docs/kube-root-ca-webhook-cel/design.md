# Design: Protect `kube-root-ca.crt` from customer webhooks via CEL `matchConditions`

Date: 2026-11-25
Branch: `fix/kube-root-ca-webhook-cel` (worktree `../gardener-kube-root-ca-cel`, off master `4122513646`)

## Intent and why
Shoot CA/credentials rotation hangs when a customer admission webhook intercepts `configmaps`
writes with `failurePolicy: Fail` and no reachable backing pods: it blocks writes to
`kube-root-ca.crt`, which exists in every namespace and has no stable label. `WaitUntilKubeRootCAConfigMapsUpdated`
then loops forever (observed live: `number:4` repeating every 30s, `RetryUntilTimeout(30s,5m)`).
Issue 9384; reproduced live on the local cluster in this session.

We extend Gardener's existing care-loop webhook remediation to append a null-safe CEL
`matchCondition` that makes the offending webhook skip exactly the object named `kube-root-ca.crt`,
in every namespace — leaving every other ConfigMap guarded and `failurePolicy` untouched.

## Goals
- Unblock rotation when a customer configmaps webhook is in the path of `kube-root-ca.crt`.
- Be surgical: exclude only `kube-root-ca.crt`, by name, across all namespaces.
- Leave the customer's policy fully enforced for every other ConfigMap.
- Reuse the existing remediation machinery (care loop, patch, warning annotation).

## Non-goals
- Fixing non-webhook rotation failures (down publisher, KCM not ready, stuck namespace).
- Labeling the system-owned `kube-root-ca.crt` object (rejected: not k8s-native).
- Cluster-wide `failurePolicy: Ignore` for configmaps (rejected: too permissive).

## Anti-goals
- Any change that could make a CEL eval error reject the write (would recreate the hang).
- Changing behavior of the existing failurePolicy / selectors / lease / timeout remediation paths.

## Constraints
- Matcher can only express GVR + scope + labels — never object name. We add a name only for the
  REMEDIATION, not for detection (detection stays coarse; remediation is name-scoped).
- CEL must be null-safe under `failurePolicy: Fail`.
- `matchConditions` GA since k8s 1.30; Gardener min shoot 1.33 → no version gate.

## Approaches considered
- **A — `ObjectNames []string` field + CEL matchConditions (CHOSEN).** New additive third
  remediation path. Smallest surgical change; leaves existing entries/paths intact; targets the
  name axis directly.
- **B — overload `ObjectLabels` to mean name when a flag is set.** Rejected: muddies the label
  path's meaning and `selectors()`.
- **C — config-driven name list.** Rejected: heavier plumbing for one known name.

Prior sibling branches for comparison: `broad` (label-less failurePolicy flip — too permissive),
`narrow` (GRM labeler — rejected non-native label), `diagnostics` (error enrichment only).

## The how (decided)
Full detail in `.tasks/kube-root-ca-webhook-cel/spec.md`. Summary:
1. `matcher.go`: add `ObjectNames []string` to `WebhookConstraintMatcher`; add
   `{GVR: configmaps, ObjectNames: []string{"kube-root-ca.crt"}}` alongside (not replacing) the
   kube-system entry. `Match()` unchanged.
2. `webhook_remediation.go`: new `mustRemediateMatchConditions`, `remediate.matchConditions`,
   idempotent `extendMatchConditions`; wired into both the Validating and Mutating loops. CEL:
   `!(request.resource.group == "" && request.resource.resource == "configmaps" && object != null && has(object.metadata.name) && object.metadata.name == "kube-root-ca.crt")`,
   condition name `gardener-exclude-kube-root-ca.crt`.
3. Guard `mustRemediateFailurePolicy` so an `ObjectNames` entry does NOT flip failurePolicy.
4. Tighten `mustRemediateSelectors` to require non-empty labels (avoids a cosmetic empty selector).

## Two decisions the orchestrator made (both open in the spec)
1. **`IsProblematicWebhook` includes the new entry** → keep the constraint/warning effect; such a
   webhook genuinely is problematic. Documented with a test.
2. **`mustRemediateSelectors` tightened** to require non-empty labels → safe precision.

## Left to the builder
- Whether the `Match` unit test lives in a new `matcher_test.go` or inline in the care test file.
- Exact table-test phrasing, mirroring existing cases.

## Verification
- Unit + lint green in `pkg/gardenlet/controller/shoot/care/...` and
  `pkg/gardenlet/operation/botanist/matchers/...`.
- Live e2e on the already-hung local cluster: enable `webhookRemediatorEnabled`, confirm the care
  loop patches the CEL condition into `block-kube-root-ca`, the `number:4` loop clears, rotation
  advances, and a differently-named ConfigMap is still blocked (proving name-only exclusion).
