# End-to-end scenario — kube-root-ca.crt webhook narrow fix (Option 4)

This is a from-scratch, live KinD + Gardener e2e that proves the "narrow" fix end to end:

1. the GRM **root-ca-cert-labeler** stamps `config.gardener.cloud/kube-root-ca=true` on every
   `kube-root-ca.crt` ConfigMap in the shoot;
2. the gardenlet care-loop **webhook remediator** adds an
   `objectSelector: config.gardener.cloud/kube-root-ca NotIn [true]` to a blocking customer
   configmaps webhook;
3. CA rotation (`rotate-ca-start`) progresses `Preparing → Prepared` because `kube-root-ca.crt`
   is writable again, **while other ConfigMaps in the shoot stay blocked** by the webhook.

> You (the human) run this — the tester cannot bring up KinD/Gardener here. Run everything from the
> NARROW worktree so the new labeler code is what gets built and deployed:
> `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow`.

---

## 0. Prerequisites: enable both switches BEFORE bringing Gardener up

Two things must be on for this scenario:

### (a) gardenlet webhook remediator

Edit `dev-setup/gardenlet/base/gardenlet.yaml` and set the remediator under the shoot-care
controller (add it if missing):

```yaml
    controllers:
      shoot:
        reconcileInMaintenanceOnly: true
        respectSyncPeriodOverwrite: true
      shootCare:
        webhookRemediatorEnabled: true   # <-- add this
```

(The field is `config.gardenlet.controllers.shootCare.webhookRemediatorEnabled`, defined in
`pkg/apis/config/gardenlet/v1alpha1/types.go`. It defaults to `false`.)

### (b) GRM root-ca-cert-labeler

No manual edit is required: for shoots, `pkg/component/gardener/resourcemanager/resource_manager.go`
sets `config.Controllers.RootCACertLabeler.Enabled = true` automatically whenever the GRM runs in
`ForShootOrVirtualGarden` responsibility mode (every shoot control plane). You only verify it is on
(step 4). If you want to double-check the rendered config later:

```bash
# in the SEED/runtime cluster, in the shoot control-plane namespace shoot--local--local
kubectl -n shoot--local--local get cm gardener-resource-manager -o yaml | grep -A2 rootCACertLabeler
# expect: rootCACertLabeler:\n  enabled: true
```

---

## 1. Bring up KinD + Gardener from the narrow worktree

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
make kind-up
make gardener-up        # = operator-up garden-up seed-up (builds the narrow code via skaffold)
```

### Two-kubeconfig setup (important)

Local Gardener uses separate kubeconfigs for the runtime/seed cluster and the virtual garden. Export
both so you can switch per command (do NOT rely on a single ambient KUBECONFIG):

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
export KUBECONFIG_GARDEN=$PWD/dev-setup/kubeconfigs/virtual-garden/kubeconfig   # Shoots, Projects
export KUBECONFIG_SEED=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig            # Seed / shoot control planes
```

Use an **inline** `KUBECONFIG=...` prefix on each command rather than a global export, e.g.
`KUBECONFIG=$KUBECONFIG_GARDEN kubectl ...`. The shoot object lives in the **garden** cluster; the
shoot's control-plane namespace (`shoot--local--local`) and the shoot's own API server live behind
the **seed** cluster.

---

## 2. Create a shoot

```bash
KUBECONFIG=$KUBECONFIG_GARDEN kubectl apply -f example/provider-local/shoot.yaml
# shoot: garden-local/local
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local wait --for=condition=APIServerAvailable \
  shoot/local --timeout=20m
```

Get an admin kubeconfig for the shoot cluster itself (where `kube-root-ca.crt` lives in every
namespace):

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
KUBECONFIG=$KUBECONFIG_GARDEN kubectl create \
  -f <(cat <<'EOF'
apiVersion: authentication.gardener.cloud/v1alpha1
kind: AdminKubeconfigRequest
spec:
  expirationSeconds: 7200
EOF
) --raw /apis/core.gardener.cloud/v1beta1/namespaces/garden-local/shoots/local/adminkubeconfig \
  | jq -r '.status.kubeconfig' | base64 -d > /tmp/shoot-local.kubeconfig
export KUBECONFIG_SHOOT=/tmp/shoot-local.kubeconfig
```

> If your Gardener version exposes the shoots/adminkubeconfig subresource differently, use
> `gardenctl target --garden local --project local --shoot local` then `gardenctl kubectl-env`.
> The goal is simply: a kubeconfig that talks to the **shoot** API server.

---

## 3. Observe the labeler (this is the startup window)

The label appears only **after** the GRM root-ca-cert-labeler has reconciled each namespace's
`kube-root-ca.crt`. Right after shoot creation there is a brief window before all namespaces are
stamped — poll until it is present:

```bash
# Every namespace in the SHOOT should have kube-root-ca.crt labeled.
KUBECONFIG=$KUBECONFIG_SHOOT kubectl get cm kube-root-ca.crt -A \
  -L config.gardener.cloud/kube-root-ca
```

Expected (after the labeler reconciles — may take a few seconds per namespace):

```
NAMESPACE         NAME               DATA   AGE   KUBE-ROOT-CA
default           kube-root-ca.crt   1      ..    true
kube-system       kube-root-ca.crt   1      ..    true
kube-public       kube-root-ca.crt   1      ..    true
kube-node-lease   kube-root-ca.crt   1      ..    true
```

Key observation (1): **every** `kube-root-ca.crt` carries `config.gardener.cloud/kube-root-ca=true`;
no other ConfigMap gets the label.

---

## 4. Plant a blocking customer configmaps webhook in the shoot

Create a `ValidatingWebhookConfiguration` in the **shoot** that intercepts all configmaps writes,
with `failurePolicy: Fail` and **no backing service/pods** — so every configmap write fails closed.

```bash
KUBECONFIG=$KUBECONFIG_SHOOT kubectl apply -f - <<'EOF'
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: block-configmaps
webhooks:
- name: block-configmaps.example.com
  admissionReviewVersions: ["v1"]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 10
  clientConfig:
    url: https://127.0.0.1:19999/validate   # nothing listens here -> always fails
  rules:
  - apiGroups: [""]
    apiVersions: ["v1"]
    resources: ["configmaps"]
    operations: ["CREATE","UPDATE"]
EOF
```

Sanity check it blocks writes (should error with a webhook-call failure):

```bash
KUBECONFIG=$KUBECONFIG_SHOOT kubectl create cm canary --from-literal=a=b -n default
# expected: Error ... failed calling webhook "block-configmaps.example.com": ... connection refused
```

---

## 5. Start CA rotation

```bash
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local annotate shoot local \
  gardener.cloud/operation=rotate-ca-start --overwrite
```

Watch the CA rotation phase on the shoot status:

```bash
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local get shoot local \
  -o jsonpath='{.status.credentials.rotation.certificateAuthorities.phase}{"\n"}'
```

Without the fix this would be stuck in `Preparing` (the control plane cannot update
`kube-root-ca.crt` because the webhook blocks it).

---

## 6. Observe remediation + unblock

### (2) The care-loop remediator narrows the webhook

The gardenlet care controller detects the blocking webhook and adds an objectSelector that excludes
the labeled `kube-root-ca.crt`. Inspect the webhook in the **shoot**:

```bash
KUBECONFIG=$KUBECONFIG_SHOOT kubectl get validatingwebhookconfiguration block-configmaps -o yaml \
  | grep -A6 objectSelector
```

Expected:

```yaml
    objectSelector:
      matchExpressions:
      - key: config.gardener.cloud/kube-root-ca
        operator: NotIn
        values:
        - "true"
```

Also expect the remediation warning annotation on the object:

```bash
KUBECONFIG=$KUBECONFIG_SHOOT kubectl get validatingwebhookconfiguration block-configmaps \
  -o jsonpath='{.metadata.annotations.gardener\.cloud/warning}{"\n"}'
# expected: a non-empty warning string explaining the remediation
```

### (3a) Rotation unblocks: Preparing -> Prepared

```bash
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local wait --for=jsonpath=\
'{.status.credentials.rotation.certificateAuthorities.phase}'=Prepared \
  shoot/local --timeout=15m
```

Expected final phase: `Prepared`. Because the webhook now has
`objectSelector NotIn [config.gardener.cloud/kube-root-ca=true]`, writes to `kube-root-ca.crt`
bypass the dead webhook and the rotation completes stage one.

### (3b) Other ConfigMaps stay blocked

The narrowing only excludes the labeled ConfigMap — all other configmaps writes still go through the
webhook and still fail:

```bash
# unlabeled configmap: STILL blocked (webhook still calls the dead endpoint)
KUBECONFIG=$KUBECONFIG_SHOOT kubectl create cm canary2 --from-literal=a=b -n default
# expected: Error ... failed calling webhook "block-configmaps.example.com": ... connection refused

# kube-root-ca.crt: NOT blocked (excluded by objectSelector) — e.g. a harmless re-label succeeds
KUBECONFIG=$KUBECONFIG_SHOOT kubectl label cm kube-root-ca.crt -n default probe=ok --overwrite
# expected: configmap/kube-root-ca.crt labeled   (no webhook error)
```

This is the precise, label-scoped behavior: rotation-critical `kube-root-ca.crt` is writable, every
other configmap remains protected by the customer webhook.

---

## 7. (optional) Finish and clean up

```bash
# complete rotation stage two if you want a full cycle
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local annotate shoot local \
  gardener.cloud/operation=rotate-ca-complete --overwrite

# tear down the webhook and the shoot
KUBECONFIG=$KUBECONFIG_SHOOT kubectl delete validatingwebhookconfiguration block-configmaps
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local annotate shoot local \
  confirmation.gardener.cloud/deletion=true --overwrite
KUBECONFIG=$KUBECONFIG_GARDEN kubectl -n garden-local delete shoot local --wait

cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
make gardener-down
make kind-down
```

---

## Relationship to the existing e2e suite

There is **no** pre-existing Ginkgo e2e that plants a failing customer webhook and asserts
label-scoped remediation during CA rotation. The closest specs are:

- `test/e2e/gardener/shoot/create_rotate_delete.go` and
  `test/e2e/gardener/shoot/internal/rotation/certificate_authorities.go` — drive CA rotation but do
  **not** install a blocking webhook, so they would still go green without this fix (they don't
  exercise the hang). They are the right place to **extend** later with a webhook-remediation case,
  but doing so needs a new rotation+webhook helper and is out of scope for a docs-only task.

Because the behavior is operational (needs a live shoot, a rotation operation, and a planted dead
webhook), the manual KinD walkthrough above is the from-scratch e2e. The existing automated target
to run the plain rotation flow (for reference) is:

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
make test-e2e-local   # runs ./test/e2e/gardener/... incl. create_rotate_delete, label-filter "default"
```

Running that target proves rotation still works with the labeler/remediator enabled (no regression),
but it does not by itself prove the narrow exclusion — the manual steps 4–6 above are what prove the
fix.
