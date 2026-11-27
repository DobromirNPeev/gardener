# Option 2 — live KinD + Gardener e2e result (CARotationPossible constraint + admission gate)

Date: 2026-11-26
Worktree: `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-opt2-carotation`
Branch: `fix/kube-root-ca-webhook-opt2-carotation`
Cluster: existing KinD `gardener-local` (reused, not recreated). NOT committed. Cluster left UP.

## What was redeployed

The cluster was running Option 1's gardenlet. Option 2 requires BOTH:
- gardenlet (computes the new `CARotationPossible` constraint + the label-less `{GVR: configmaps}`
  matcher entry in `pkg/gardenlet/operation/botanist/matchers/matcher.go`)
- gardener-apiserver (admission enforcement in `pkg/api/core/validation/shoot.go`
  `validateCARotationPossible`, wired into `validateShootOperationContext` for `rotate-ca-start`
  and `rotate-credentials-start`).

Redeploy performed from the Option 2 worktree with a single:

    make gardener-up        # == operator-up + garden-up + seed-up (reused the running KinD cluster)

- `operator-up` + `garden-up` redeployed the gardener-operator-managed virtual garden control plane,
  including **gardener-apiserver** (new pods, Option 2 validation).
- `seed-up` rebuilt and redeployed **gardenlet** (new pod, Option 2 constraint controller + matcher).

Verified fresh pods afterwards: `gardener-apiserver-*` (~3m old) and `gardenlet-*` (~100s old).

Prep: copied the 15 gitignored `dev-setup/` artifacts (kubeconfigs + registry TLS + patches) from the
opt1 worktree so skaffold could target the running cluster. Runtime API port had not drifted.

## Cleanup before testing

- Deleted leftover `ValidatingWebhookConfiguration block-configmaps-non-kube-system` in the shoot.
- Completed the in-flight CA rotation (was at phase `Prepared`): `rotate-ca-complete` ->
  rotation phase `Completed`, lastOperation `Succeeded`. (This `rotate-ca-complete` was accepted by
  the new Option 2 gardener-apiserver — the gate only blocks `*-start`, not `*-complete`.)
- Confirmed clean baseline: no `CARotationPossible` constraint in status (True is filtered out), shoot
  healthy/Reconcile Succeeded.

## PHASE A — constraint trips + rotation forbidden (the fix working)

1. Planted customer webhook `customer-configmaps-guard` in the shoot: `failurePolicy: Fail`, 10s
   timeout, `configmaps` CREATE/UPDATE, scope `*`, `namespaceSelector` excluding kube-system, pointing
   at a non-existent backend service (no backing pods). Represents the incident: it does not touch
   kube-system/kube-root-ca.crt but blocks the per-customer-namespace kube-root-ca.crt ConfigMaps.

2. After the care cycle (~30s) the constraint appeared as **False**:

       CARotationPossible=False (ProblematicWebhooks)
       codes: [ERR_PROBLEMATIC_WEBHOOK]
       message: ValidatingWebhookConfiguration "customer-configmaps-guard" is problematic:
         webhook "configmaps.customer.example.com" with failurePolicy "Fail" and 10s timeout
         might prevent worker nodes from properly joining the shoot cluster

   (HibernationPossible and MaintenancePreconditionsSatisfied also flipped to False with the same
   reason — expected, they share the `CheckForProblematicWebhooks` result.)

3. `kubectl annotate shoot local gardener.cloud/operation=rotate-ca-start` was **REJECTED** by
   admission (exit 1). Exact rejection:

       The Shoot "local" is invalid: metadata.annotations[gardener.cloud/operation]: Forbidden:
       cannot start CA rotation while the "CARotationPossible" constraint is 'False': a problematic
       webhook may block updates to the kube-root-ca.crt ConfigMaps and make the rotation hang;
       please fix the webhook first (see .status.constraints for details)

   The operation annotation was NOT applied and rotation phase stayed `Completed` — clean reject,
   no partial rotation started.

## PHASE B — constraint clears + rotation allowed (control)

4. Deleted the webhook. After the next care cycle the `CARotationPossible` constraint cleared
   (went True -> filtered out of status, i.e. ABSENT). HibernationPossible and
   MaintenancePreconditionsSatisfied returned to True (NoProblematicWebhooks).

5. `rotate-ca-start` retried -> **ACCEPTED** (exit 0, annotation applied). Rotation actually
   proceeded: phase progressed Preparing -> Prepared -> (rotate-ca-complete) -> Completing ->
   Completed, lastOperation Succeeded. Shoot left stable and healthy.

## Make-or-break verdict

- Did the `CARotationPossible` constraint actually appear in status? **YES — status False,
  reason ProblematicWebhooks, naming the customer webhook.**
- Did the admission plugin actually reject? **YES — forbidden error from validateCARotationPossible,
  rotation did not start.**
- Control (Phase B) acceptance after fixing the webhook: **YES.**

## Limitations / notes

- Care-cycle timing window: the constraint is only recomputed on the shoot care sync (~30s). There is
  a short window after planting/removing the webhook where the constraint is briefly stale, so the
  admission gate can lag reality for up to one care cycle. In Phase A the constraint took a couple of
  cycles to appear.
- The `CARotationPossible` constraint is driven by exactly the same `CheckForProblematicWebhooks`
  result as HibernationPossible/MaintenancePreconditionsSatisfied, so any webhook those flag will also
  flag it; the catch of the customer **configmaps** webhook specifically depends on the label-less
  `{GVR: configmaps}` matcher entry added in Option 2 (confirmed present and working — without it the
  customer webhook is invisible, as documented in the two-phase scenario).
- When True, the constraint is intentionally dropped from `.status.constraints`
  (`filterOptionalConstraints`), so absence == allowed.
- After the final CA rotation the previously generated shoot admin kubeconfig (`admin-kubeconf.yaml`)
  is stale (its client cert is signed by the old CA); regenerate via
  `hack/usage/generate-kubeconfig.sh` if the shoot needs to be reached again.

Cluster left UP for the next option. Nothing committed.
