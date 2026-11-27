# E2E scenario (summary / pointer): CEL `matchConditions` remediation for `kube-root-ca.crt`

This is a **concise pointer** for the live, from-scratch end-to-end test. It does NOT duplicate the
procedure — the authoritative, step-by-step from-scratch run (empty machine → KinD → Gardener →
shoot → blocking webhook → CA rotation → proof) lives in:

- **`./e2e-plan.md`** — the full from-scratch procedure (run this).
- **`./e2e-run-log.md`** — a captured run log / evidence from a previous execution.

The tester cannot run this (no live KinD cluster in the authoring environment); the user runs it.
Everything is executed **from the CEL worktree** so skaffold builds the branch code:
```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel
```

---

## One-line flow (see `e2e-plan.md` for exact commands)

1. Enable the remediator: `spec.config.controllers.shootCare.webhookRemediatorEnabled: true` in
   `dev-setup/gardenlet/base/gardenlet.yaml` (already set on this branch).
2. `make kind-up` then `make gardener-up` (builds gardenlet from this worktree's changed packages).
3. Create the example shoot (`example/provider-local/shoot.yaml`) and wait until Ready.
4. Plant a fail-closed `configmaps` CREATE/UPDATE `ValidatingWebhookConfiguration` inside the shoot
   (no backing pod ⇒ it blocks). Confirm it denies a probe ConfigMap create.
5. Trigger `gardener.cloud/operation=rotate-ca-start` on the shoot.
6. Watch the gardenlet care loop remediate the webhook (`fieldName:"matchConditions"`).

---

## Key pass criteria (the proof)

| # | Expectation |
|---|---|
| 1 | **gardenlet writes** the matchCondition `gardener-exclude-kube-root-ca.crt` with the null-safe CEL `!(request.resource.group == "" && request.resource.resource == "configmaps" && object != null && has(object.metadata.name) && object.metadata.name == "kube-root-ca.crt")` |
| 2 | **`failurePolicy` stays `Fail`** (not flipped to `Ignore`) and the webhook's selectors are untouched |
| 3 | **Other ConfigMaps are still blocked** by the webhook (name-scoped, not fail-open); only `kube-root-ca.crt` becomes writable |
| 4 | Remediation is **idempotent** — `resourceVersion` of the webhook config is stable across care cycles |
| 5 | **The hang clears** — `kube-root-ca.crt` gets the 2-cert `[old,new]` bundle + confirmation label in all namespaces, the wait task Succeeds, and the shoot leaves `Preparing` → `Prepared` |

These mirror `e2e-plan.md` sections 7–9. If all hold, the production gardenlet code (not a
hand-applied YAML) detects the blocking configmaps webhook and unblocks the `kube-root-ca.crt`
rotation hang live, end-to-end.

---

## No new make target needed

This reuses the standard local dev stack targets (`make kind-up` / `make gardener-up` /
`make gardener-down` / `make kind-down`) described in `e2e-plan.md`; there is no dedicated Ginkgo
`test-e2e-local-*` spec for this operational/rotation flow, and `e2e-plan.md` drives it by hand
against the local KinD cluster. Do not invent a separate harness.
