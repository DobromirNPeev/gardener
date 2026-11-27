# Option 2 (CARotationPossible constraint + admission gate) — Go test scenarios

This document describes the runnable Go unit/integration tests that prove Option 2, in two phases:

- **Phase 1 — prove the gap** (as-built, before the detection-only `configmaps` matcher was added):
  a customer `configmaps` webhook scoped to a non-`kube-system` namespace (the real "kube-root-ca.crt"
  incident) is **not** detected as problematic, so `CARotationPossible` is never set `False` and the
  admission gate does **not** block `rotate-ca-start`.
- **Phase 2 — prove the fix**: after adding a single label-less `{GVR: configmaps}` matcher, the same
  webhook is detected as problematic → `CARotationPossible` goes `False` (once the shoot is stable) →
  `validateCARotationPossible` rejects `rotate-ca-start`/`rotate-credentials-start` with a forbidden error.

## The minimal Phase 2 code change

`pkg/gardenlet/operation/botanist/matchers/matcher.go`, in `WebhookConstraintMatchers`, right after the
existing kube-system-scoped configmaps entry:

```go
// Leader election of kube-controller-manager, ...
{GVR: corev1.SchemeGroupVersion.WithResource("configmaps"), NamespaceLabels: kubeSystemNamespaceLabels},
// Detection-only entry for the per-namespace "kube-root-ca.crt" ConfigMaps: a customer webhook on
// configmaps with failurePolicy=Fail in any namespace would block gardenlet from updating these
// ConfigMaps during a CA rotation and make the rotation hang. A label-less entry matches configmaps
// CREATE/UPDATE webhooks regardless of namespace or object scoping.
{GVR: corev1.SchemeGroupVersion.WithResource("configmaps")},
```

This is detection-only. No CEL / `ObjectNames` / remediation machinery is involved (those belong to
Option 3). A label-less matcher has `ObjectLabels == nil` and `NamespaceLabels == nil`, so
`WebhookConstraintMatcher.Match` treats it as "match all objects and all namespaces" and returns `true`
for any `configmaps` CREATE/UPDATE webhook.

## Exact commands

```bash
cd <worktree-root>   # gardener-kube-root-ca-opt2-carotation

# All four packages in scope:
go test ./pkg/apis/core/... ./pkg/api/core/validation/... \
        ./pkg/gardenlet/controller/shoot/care/... \
        ./pkg/gardenlet/operation/botanist/matchers/...

go vet  ./pkg/apis/core/... ./pkg/api/core/validation/... \
        ./pkg/gardenlet/controller/shoot/care/... \
        ./pkg/gardenlet/operation/botanist/matchers/...
```

Focused runs used during development:

```bash
# webhook detection (constraints package)
go test ./pkg/gardenlet/controller/shoot/care/ -run TestCare -ginkgo.focus="kube-root-ca.crt incident"
go test ./pkg/gardenlet/controller/shoot/care/ -run TestCare -ginkgo.focus="CARotationPossible"
# matcher package
go test ./pkg/gardenlet/operation/botanist/matchers/...
# admission gate (validation package)
go test ./pkg/api/core/validation/ -run TestValidation -ginkgo.focus="starting CA rotation"
```

Expected final output (all packages):

```
ok  github.com/gardener/gardener/pkg/apis/core
ok  github.com/gardener/gardener/pkg/apis/core/v1
ok  github.com/gardener/gardener/pkg/apis/core/v1beta1
ok  github.com/gardener/gardener/pkg/api/core/validation
ok  github.com/gardener/gardener/pkg/gardenlet/controller/shoot/care
ok  github.com/gardener/gardener/pkg/gardenlet/operation/botanist/matchers
```

## Phase 1 — proving the gap (as-built)

Before the matcher was added, running the new table
`#IsProblematicWebhook - configmaps / kube-root-ca.crt incident` in
`pkg/gardenlet/controller/shoot/care/constraints_test.go` produced **2 failures**:

```
[FAIL] ... [It] CREATE, namespaceSelector scoped to a customer namespace
  [FAILED] expected the configmaps webhook to be detected as problematic
[FAIL] ... [It] UPDATE, namespaceSelector excluding kube-system
  [FAILED] expected the configmaps webhook to be detected as problematic
```

This is the documented gap: a `configmaps` webhook scoped away from `kube-system` was invisible to
`IsProblematicWebhook`, so Option 2's constraint+gate never engaged for the real incident.

Note (empirically verified on this branch): a `configmaps` webhook with **no** namespace selector was
already detected before the fix (the existing kube-system-scoped matcher matches an empty/absent
namespace selector). The gap was specifically for webhooks scoped to a **non-kube-system** namespace.

## Phase 2 — proving the fix

After adding the label-less matcher the same table passes, and all of the following assert the fix:

### 1. Webhook detection — `IsProblematicWebhook`
File: `pkg/gardenlet/controller/shoot/care/constraints_test.go`
Describe: `#IsProblematicWebhook - configmaps / kube-root-ca.crt incident`

- `CREATE, namespaceSelector scoped to a customer namespace` → problematic (was the gap).
- `UPDATE, namespaceSelector excluding kube-system` → problematic (was the gap).
- `CREATE, no selectors` → problematic (regression guard; already problematic before).
- `UPDATE, objectSelector scoped to a customer app` → problematic.
- `should not be problematic on DELETE only` → a DELETE-only configmaps webhook does not block rotation.

The existing per-resource `configmaps` table was switched from `kubeSystemNamespaceTables` to
`withoutSelectorsTables`, because a `configmaps` webhook is now problematic regardless of selectors.
The `Ignore` + low-timeout escape hatch still makes such a webhook non-problematic (that logic runs
before the matcher loop).

### 2. Matcher source coverage — `WebhookConstraintMatchers`
File: `pkg/gardenlet/operation/botanist/matchers/matcher_test.go` (new suite)
Asserts the label-less `{GVR: configmaps}` entry matches configmaps CREATE/UPDATE rules regardless of
namespace/object selectors, and does not match DELETE-only.

### 3. Constraint plumbing end-to-end — `Constraint.Check`
File: `pkg/gardenlet/controller/shoot/care/constraints_test.go`
Context: `Constraint` → `#Check` → `#CARotationPossible`

- No problematic webhook → `CARotationPossible` is dropped (optional constraint that is `True` is
  filtered out by `filterOptionalConstraints`). This also confirms the constraint+gate plumbing works
  for webhooks the matchers already cover (the webhook check is shared across all matchers).
- Customer `configmaps` webhook (non-kube-system) on a shoot with no stable last operation →
  `CARotationPossible` surfaces as `Progressing` with reason `ProblematicWebhooks` (because
  `PardonConditions` pardons a freshly-`False` condition while the last operation is unstable/absent).
- Same webhook on a shoot with `LastOperation = Reconcile/Succeeded` (stable) → `CARotationPossible` is
  a strict `False` with reason `ProblematicWebhooks`. This is the state the admission gate depends on.

### 4. Admission gate — `validateCARotationPossible`
File: `pkg/api/core/validation/shoot_test.go`
Tables: `starting CA rotation` (`rotate-ca-start`) and `starting rotation of all credentials`
(`rotate-credentials-start`). New entries:

- `CARotationPossible == False` → `rotate-ca-start` / `rotate-credentials-start` is **Forbidden**.
- `CARotationPossible == True` → allowed.
- `CARotationPossible == Progressing` (pardoned, not strictly `False`) → allowed. This documents that
  the gate only engages once the constraint is a strict `False`, i.e. on a stable shoot — which is
  exactly when a user triggers a CA rotation.

## Interplay note (Progressing vs False) — not a bug, but documented

`Constraint.Check` runs the webhook check (which returns `ConditionFalse` for a problematic webhook) and
then `PardonConditions`. `PardonConditions` converts a `False` condition to `Progressing` while the
shoot's last operation is unstable (`Create`/`Delete` not succeeded) or absent. The admission gate
`validateCARotationPossible` fires only on a strict `False`. These two align in practice: a user starts
a CA rotation on a successfully created/reconciled shoot (stable last operation), where the condition is
a strict `False`, so the gate blocks. During an unstable create/delete, `rotate-ca-start` is already
rejected by the pre-existing "shoot must be created successfully" checks, so there is no bypass.
