# Option 2 — live KinD + Gardener end-to-end scenario (user-run)

This scenario brings up a local Gardener **from the Option 2 worktree**, creates a shoot, plants a
customer `configmaps` webhook that reproduces the "kube-root-ca.crt" incident, and then shows:

- **Phase 1 (as-built, label-less `configmaps` matcher NOT yet added):** `rotate-ca-start` is **not**
  blocked — the webhook is invisible to the constraint, so the admission gate stays open.
- **Phase 2 (matcher added, gardenlet rebuilt):** the shoot gets `CARotationPossible = False` in
  `.status.constraints`, and `kubectl annotate shoot ... rotate-ca-start` is **rejected** by admission
  with the forbidden error.

> This runs against a local KinD cluster via the documented `make` targets. It does not provision any
> external infrastructure.

## 0. Prerequisites and the two-kubeconfig setup

Local Gardener uses separate kubeconfigs for the **runtime/seed** cluster and the **virtual garden**
cluster. The Makefile exports them:

- Runtime (KinD) cluster: `$(DEV_SETUP)/kubeconfigs/runtime/kubeconfig`
  → env var `KUBECONFIG_RUNTIME_CLUSTER`
- Virtual garden cluster: `$(DEV_SETUP)/kubeconfigs/virtual-garden/kubeconfig`
  → env var `KUBECONFIG_VIRTUAL_GARDEN_CLUSTER`

(`DEV_SETUP` is `dev-setup/gardener-local` under the repo; confirm with `make -p | grep DEV_SETUP`.)

Shoots are created/annotated against the **virtual garden**. Pods/webhooks inside the shoot and the
shoot's `.status` constraints are observed against the **shoot's** kubeconfig (obtained via an
`adminkubeconfig` request). Keep these distinct — mixing them is the most common mistake.

Export convenience variables (adjust `REPO` to the worktree root):

```bash
export REPO=/path/to/gardener-kube-root-ca-opt2-carotation
export GARDEN_KUBECONFIG=$REPO/dev-setup/gardener-local/kubeconfigs/virtual-garden/kubeconfig
export RUNTIME_KUBECONFIG=$REPO/dev-setup/gardener-local/kubeconfigs/runtime/kubeconfig
```

## 1. Bring up local Gardener from the Option 2 worktree

```bash
cd "$REPO"
make kind-up
make gardener-up        # operator-up + garden-up + seed-up; builds & deploys gardenlet via skaffold
```

`make` sets the right `KUBECONFIG` per target internally, so no inline `KUBECONFIG=` is needed for
`kind-up` / `gardener-up`. For raw `kubectl` against the garden, pass the kubeconfig inline (see below).

## 2. Create a shoot and wait until it is healthy

Use the local example shoot (adjust the path/namespace to your checkout):

```bash
kubectl --kubeconfig "$GARDEN_KUBECONFIG" apply -f "$REPO/example/provider-local/shoot.yaml"
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local get shoot local -w
# wait until .status.lastOperation is Reconcile/Succeeded (the "stable" state the gate depends on)
```

Get the shoot admin kubeconfig (short-lived) to talk to the shoot API server:

```bash
cat > /tmp/adminkubeconfig-request.json <<'EOF'
{"apiVersion":"authentication.gardener.cloud/v1alpha1","kind":"AdminKubeconfigRequest",
 "spec":{"expirationSeconds":3600}}
EOF

kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local \
  create -f /tmp/adminkubeconfig-request.json \
  --raw "/apis/core.gardener.cloud/v1beta1/namespaces/garden-local/shoots/local/adminkubeconfig" \
  | jq -r '.status.kubeconfig' | base64 -d > /tmp/shoot-kubeconfig
export SHOOT_KUBECONFIG=/tmp/shoot-kubeconfig
```

## 3. Plant the problematic customer `configmaps` webhook in the shoot

This reproduces the real incident: a `configmaps` CREATE/UPDATE webhook with `failurePolicy: Fail`
scoped to a non-`kube-system` namespace, pointing at a backend that does not answer.

```bash
cat > /tmp/configmaps-webhook.yaml <<'EOF'
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: customer-configmaps-webhook
webhooks:
- name: configmaps.customer.example.com
  admissionReviewVersions: ["v1"]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 30
  namespaceSelector:
    matchExpressions:
    - key: kubernetes.io/metadata.name
      operator: NotIn
      values: ["kube-system"]
  rules:
  - apiGroups: [""]
    apiVersions: ["v1"]
    operations: ["CREATE", "UPDATE"]
    resources: ["configmaps"]
    scope: Namespaced
  clientConfig:
    # unreachable backend → requests fail under failurePolicy: Fail
    url: https://10.255.255.1:9443/validate
EOF

kubectl --kubeconfig "$SHOOT_KUBECONFIG" apply -f /tmp/configmaps-webhook.yaml
```

## Phase 1 — as-built (label-less `configmaps` matcher NOT added): rotation is NOT blocked

With the matcher absent, the shoot care controller does not detect this webhook, so the constraint is
never set and the admission gate is open.

```bash
# Constraint is NOT present/False (either absent or True):
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local get shoot local \
  -o jsonpath='{range .status.constraints[*]}{.type}={.status}{"\n"}{end}'
# → no "CARotationPossible=False" line

# rotate-ca-start is accepted by admission (BUG — the rotation will later hang):
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local annotate shoot local \
  gardener.cloud/operation=rotate-ca-start --overwrite
# → shoot.core.gardener.cloud/local annotated
```

Expected Phase 1 result: the annotation is **accepted**. This is the documented gap — Option 2 as built
does not block the real incident. (Clean up before Phase 2: remove the annotation / let it settle, or
recreate the shoot.)

```bash
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local annotate shoot local \
  gardener.cloud/operation- || true
```

## Phase 2 — add the matcher, rebuild gardenlet: rotation is REJECTED

Apply the one-line detection-only matcher (see `test-scenario.md` for the exact diff) in
`pkg/gardenlet/operation/botanist/matchers/matcher.go`, then rebuild and redeploy gardenlet from this
worktree:

```bash
cd "$REPO"
make gardener-up        # re-runs skaffold; rebuilds gardenlet image with the matcher change
# (seed-up alone also redeploys gardenlet if operator/garden are already up)
```

Re-plant the webhook if you recreated the shoot (step 3), then wait for the next care reconcile and
observe the constraint flip to `False`:

```bash
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local get shoot local \
  -o jsonpath='{range .status.constraints[*]}{.type}={.status}  {.reason}{"\n"}{end}'
# → CARotationPossible=False  ProblematicWebhooks
```

Now `rotate-ca-start` is rejected by the gardener-apiserver admission validation:

```bash
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local annotate shoot local \
  gardener.cloud/operation=rotate-ca-start --overwrite
```

Expected Phase 2 output (forbidden error):

```
Error from server (Forbidden): ... metadata.annotations[gardener.cloud/operation]: Forbidden:
cannot start CA rotation while the "CARotationPossible" constraint is 'False': a problematic webhook
may block updates to the kube-root-ca.crt ConfigMaps and make the rotation hang; please fix the
webhook first (see .status.constraints for details)
```

The same applies to `rotate-credentials-start` (and the `-without-workers-rollout` variants).

Verify removing the webhook re-opens the gate:

```bash
kubectl --kubeconfig "$SHOOT_KUBECONFIG" delete validatingwebhookconfiguration customer-configmaps-webhook
# wait for the next care reconcile; CARotationPossible drops/goes True
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local annotate shoot local \
  gardener.cloud/operation=rotate-ca-start --overwrite
# → accepted again
```

## 4. Teardown

```bash
cd "$REPO"
kubectl --kubeconfig "$GARDEN_KUBECONFIG" -n garden-local delete shoot local --wait
make gardener-down
make kind-down
```

## Notes

- There is no dedicated `make test-e2e-local-*` Ginkgo target that plants a raw customer webhook and
  asserts admission rejection, so this scenario is a scripted manual run against the standard local
  setup. The automated proof of Option 2 is the Go unit/integration suite in `test-scenario.md`.
- `make` injects the correct `KUBECONFIG` per target; only raw `kubectl` calls need an explicit
  `--kubeconfig`. The garden-side operations use the **virtual garden** kubeconfig; webhook planting and
  constraint observation inside the shoot use the **shoot admin** kubeconfig.
- The `CARotationPossible` constraint surfaces as a strict `False` only once the shoot's last operation
  is stable (e.g. `Reconcile/Succeeded`); while an operation is still in progress it may show as
  `Progressing` (pardoned). Trigger `rotate-ca-start` on a settled shoot to see the gate engage.
