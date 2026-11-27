# Test scenario (locally runnable): CEL `matchConditions` remediation for `kube-root-ca.crt`

This is the **locally runnable** proof for "Option 3" — the name-scoped CEL `matchCondition`
remediation path. It lists exactly which unit tests cover the feature, the `go test` commands to run
them, what each test asserts, and the expected pass output.

- Branch: `fix/kube-root-ca-webhook-cel`
- Worktree: `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel`

Run everything from the worktree root:
```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel
```

---

## What is under test

Two packages carry the Option 3 logic:

| File | Role |
|------|------|
| `pkg/gardenlet/operation/botanist/matchers/matcher.go` | adds `ObjectNames []string` to `WebhookConstraintMatcher` and the matcher entry `{GVR: configmaps, ObjectNames: ["kube-root-ca.crt"]}`. `ObjectNames` does NOT participate in `Match()` (admission rules are name-blind). |
| `pkg/gardenlet/controller/shoot/care/webhook_remediation.go` | `mustRemediateMatchConditions`, `remediator.matchConditions()`, `newExcludeObjectNameMatchCondition` (null-safe CEL), idempotent `extendMatchConditions`, guarded `mustRemediateFailurePolicy`, tightened `mustRemediateSelectors`. |

---

## 1. Run the tests

Both packages in one go:
```bash
go test -count=1 ./pkg/gardenlet/controller/shoot/care/... ./pkg/gardenlet/operation/botanist/matchers/...
```

`go vet` (no findings expected):
```bash
go vet ./pkg/gardenlet/controller/shoot/care/... ./pkg/gardenlet/operation/botanist/matchers/...
```

Per-package with Ginkgo spec counts:
```bash
go test -count=1 -v ./pkg/gardenlet/operation/botanist/matchers/...        # 3 of 3 Specs
go test -count=1 -v ./pkg/gardenlet/controller/shoot/care/...              # 1421 of 1421 Specs
```

> `-count=1` disables the Go test cache so you see a real run, not `(cached)`.

---

## 2. Which tests cover Option 3, and what each asserts

### A. `matchers/matcher_test.go` — `Describe("Matcher") > Describe("#Match")` (3 specs)

Proves the matcher entry is **name-blind**: `ObjectNames` is metadata for remediation only and never
changes which webhooks `Match()` selects.

- **`should match a configmaps CREATE rule (name-blind detection)`** — the configmaps matcher with
  `ObjectNames: ["kube-root-ca.crt"]` matches a plain `configmaps` CREATE rule with `nil` selectors.
- **`should NOT match a secrets rule even though ObjectNames is set`** — GVR still gates `Match()`;
  a `secrets` rule is not matched by the configmaps matcher.
- **`should match configmaps in all namespaces/objects (ObjectNames does not narrow Match)`** — with
  arbitrary object/namespace selectors on the rule, `Match()` still returns true; `ObjectNames` does
  not disturb object/namespace matching semantics.

### B. `care/webhook_remediation_test.go` — matchConditions remediation

- **`should remediate a configmaps-catching webhook via matchConditions (name-scoped CEL)`**
  (top-level `It`, ~line 136). Realistic webhook: catches `*/*` CREATE/UPDATE in all namespaces
  except kube-system/default, `failurePolicy: Fail`. After `Remediate`:
  - `MatchConditions` contains exactly `gardener-exclude-kube-root-ca.crt` with the null-safe CEL
    `!(request.resource.group == "" && request.resource.resource == "configmaps" && object != null && has(object.metadata.name) && object.metadata.name == "kube-root-ca.crt")`;
  - **`FailurePolicy` is left `Fail` (untouched)**;
  - the existing **`NamespaceSelector` is untouched**, `ObjectSelector` stays `nil`;
  - the `gardener.cloud/warning` annotation is set.
  This is the key assertion that name-scoped remediation is strictly additive to `matchConditions`
  and does NOT weaken `failurePolicy` or selectors.

- **`Context("validating") > It("matchConditions for configmaps")`** (~line 358). With the matcher
  list pinned (via `test.WithVar`) to only the configmaps `ObjectNames` entry and a configmaps
  CREATE/UPDATE webhook with `failurePolicy: Fail`: after `Remediate`, `MatchConditions` equals the
  expected CEL condition, `FailurePolicy` stays `Fail`, both selectors stay `nil`, warning annotation
  present.

- **`Context("validating") > It("matchConditions for configmaps is idempotent (Remediate twice does not duplicate)")`**
  (~line 387). Calls `Remediate` **twice**; asserts `MatchConditions` still `ConsistOf` a single
  `gardener-exclude-kube-root-ca.crt` entry — proving `extendMatchConditions` is idempotent across
  care-loop cycles.

- **`Context("mutating") > It("matchConditions for configmaps")`** (~line 653) and
  **`Context("mutating") > It("... is idempotent ...")`** (~line 682). Same two assertions for a
  `MutatingWebhookConfiguration`, proving the remediation path is identical for mutating webhooks.

### C. `care/constraints_test.go` — detection via shoot constraints

The configmaps resource is switched from `kubeSystemNamespaceTables` to a dedicated
`configMapsTestTables` (diff ~line 335). Because the `ObjectNames` matcher has `nil`
`NamespaceLabels`, a configmaps CREATE/UPDATE webhook with `failurePolicy: Fail` is now problematic
**in every namespace** — even with a `namespaceSelector` that excludes `kube-system`, or one that
matches `foo=bar`. The added entries (`configMapsNamespaceProblematic`) assert these previously
"not problematic" selector cases are now reported as problematic constraints for configmaps, while
every other kube-system-scoped resource keeps the original shared not-problematic list untouched.

---

## 3. Expected pass output

Combined run:
```
ok  	github.com/gardener/gardener/pkg/gardenlet/controller/shoot/care	1.605s
ok  	github.com/gardener/gardener/pkg/gardenlet/operation/botanist/matchers	1.003s
```

`go vet`: no output, exit code 0.

Ginkgo spec counts (verbose):
```
# matchers
Ran 3 of 3 Specs in 0.001 seconds
SUCCESS! -- 3 Passed | 0 Failed | 0 Pending | 0 Skipped

# care
Ran 1421 of 1421 Specs in 0.602 seconds
```

All green ⇒ the CEL condition is written, `failurePolicy` and `objectSelector` are left untouched,
remediation is idempotent, `Match()` stays name-blind, and the constraint detector now flags any
blocking configmaps webhook — all verified in-package without a cluster.
