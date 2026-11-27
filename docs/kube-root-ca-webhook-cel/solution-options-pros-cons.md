# Solution options for the `kube-root-ca.crt` rotation hang — pros & cons

**Problem.** Shoot CA/credentials rotation hangs when a customer admission webhook on `configmaps`
(`failurePolicy: Fail`, no reachable backing pods) blocks writes to the per-namespace
`kube-root-ca.crt` ConfigMap. gardenlet's `WaitUntilKubeRootCAConfigMapsUpdated`
(`pkg/gardenlet/operation/botanist/kubecontrollermanager.go:90`) then loops forever
(`number:N`, `RetryUntilTimeout(30s,5m)`), rotation stuck in `Preparing`.

**Origin.** Introduced by:
- issue https://github.com/gardener/gardener/issues/15301
- PR https://github.com/gardener/gardener/pull/15430 (`af06df8eaa` — "Wait for `kube-root-ca.crt`
  ConfigMaps to contain the new CA bundle before rolling workers"). This PR added the all-namespace
  wait that a customer webhook can block.

The five candidate options from the issue, weighed below.

---

## Option 1 — Wait only for `kube-system`'s `kube-root-ca.crt`

Narrow the wait loop from "all namespaces" back to just `kube-system`.

**Pros**
- Smallest change — one query in `kubecontrollermanager.go` (the regression point from #15430).
- Shrinks the blast radius: fewer ConfigMaps in the critical path = less chance a customer webhook
  sits in front of the one you wait on.
- No new API, no remediation, no webhook mutation.

**Cons**
- Doesn't fix the problem — it **hides** it. The other namespaces still need the new bundle; you just
  stop verifying them. If a webhook blocks `default/kube-root-ca.crt`, pods there mount a stale CA →
  trust breakage you no longer detect.
- Weakens the correctness guarantee PR #15430 deliberately added (bundle propagated everywhere before
  rolling workers).
- A webhook with `scope: *` still blocks the kube-system one too — doesn't reliably dodge the incident.

---

## Option 2 — `CARotationPossible` constraint; forbid rotation if false

Add a new constraint (mirroring `HibernationPossible`), computed from `IsProblematicWebhook`; block
`rotate-ca-start` when it is false.

**Pros**
- Prevents the hang **before it starts** — you never enter a rotation that will get stuck.
- Reuses existing machinery (`constraints.go`, `IsProblematicWebhook`, `WebhookConstraintMatchers`).
- Clear, actionable signal to the customer ("fix your webhook first") instead of a mysterious stall.
- Fail-safe: refuses the dangerous op rather than attempting it and wedging.

**Cons**
- Doesn't fix anything — it just **gates**. The customer must still manually fix the webhook; rotation
  is blocked, not enabled. If the CA genuinely needs rotating (expiry), you are now stuck the other way.
- New constraint = API surface, admission validation for the rotation annotation, docs.
- Detection is coarse (name-blind `Match`), so it may block rotation for webhooks that would not
  actually hit `kube-root-ca.crt` — false positives that forbid a safe rotation.

---

## Option 3 — Adapt matchers + remediate via CEL `matchConditions`  ← IMPLEMENTED

New `ObjectNames []string` field on `WebhookConstraintMatcher` + a name-scoped CEL exclusion added by
the care-loop remediator. (Branch `fix/kube-root-ca-webhook-cel`; verified live, `Preparing → Prepared`.)

CEL written onto the offending webhook:
```
!(request.resource.group == "" && request.resource.resource == "configmaps"
  && object != null && has(object.metadata.name) && object.metadata.name == "kube-root-ca.crt")
```

**Pros**
- **Actually unblocks rotation automatically** — no manual step (proven end-to-end in the e2e).
- **Surgical**: excludes only `kube-root-ca.crt` by name; the webhook keeps guarding every other
  ConfigMap; `failurePolicy` is untouched (NOT fail-open).
- No persistent artifact on the system object (unlike Option 4); no new API type; small additive diff
  (2 production files).
- CEL `matchConditions` is GA on all supported shoots (min 1.33) — no version gate.
- Idempotent (dedup by condition name; resourceVersion stable across care cycles).

**Cons**
- Only effective when `webhookRemediatorEnabled` is on (off by default) — landscapes without it still hang.
- **Mutates the customer's webhook** — intrusive; some operators dislike Gardener editing customer
  objects (mitigated by the `gardener.cloud/warning` annotation + the opt-out exclude label).
- Eventually-consistent: the care loop runs on its own cadence, so the first rotation attempt can still
  fail before remediation lands (recovers on the next reconcile).
- Minor: per-cycle "Remediating" log noise (idempotent write, but logs each cycle); the webhook
  constraint still flips `False` even after remediation (reason `RemediatedWebhooks`).

---

## Option 4 — Stable label on the ConfigMap + remediate to ignore that label

A GRM controller stamps a well-known label on `kube-root-ca.crt`; the remediator adds
`objectSelector: <label> NotIn [true]` so the webhook skips the labeled object. (The earlier NARROW branch.)

**Pros**
- Also surgical (excludes only the labeled object); the webhook keeps guarding the rest.
- Uses the existing selector remediation path — no CEL needed.
- The label is a stable, declarative marker other tooling could reuse.

**Cons**
- **Rejected:** puts a persistent label on a **system-owned** object (`kube-root-ca.crt`) — not
  k8s-native, conflicts with GitOps/policy engines, and the upstream `root-ca-cert-publisher` gives no
  guarantee the label is preserved.
- Needs a **net-new GRM controller** + config + RBAC to stamp the label — far larger surface than Option 3.
- Startup window: until the labeler reconciles, the object is unlabeled → unprotected.
- Still gated by `webhookRemediatorEnabled`, same as Option 3.

---

## Summary

| Option | What it does | Fixes or hides? | Surface |
|---|---|---|---|
| 1 — kube-system only | shrinks what you verify | **hides** (weakens correctness) | tiny |
| 2 — `CARotationPossible` | refuses to start rotation | **prevents** (but blocks legit rotations) | medium (new constraint + admission) |
| **3 — CEL matchConditions** | auto-unblocks, surgical | **fixes** | small (2 files, no API) |
| 4 — stable label | auto-unblocks via label | fixes, but intrusive on system object | large (new controller + RBAC) |

**Recommendation.** They attack different points and the strongest answer combines two:

- **Option 3 as the fix** — the only option that *fixes* the hang surgically, automatically, without a
  persistent system-object mutation. Implemented and verified live.
- **Option 2 as the complement/guard** — the `CARotationPossible` constraint warns (and optionally
  gates) so that in landscapes where the remediator is OFF, you do not walk into the hang blind.

3 fixes; 2 warns. Together they cover both the "remediator on" and "remediator off" worlds.

Drop **Option 1** (it masks a real correctness requirement from #15430). **Option 4** is ruled out on
the non-native-system-object-label grounds.
