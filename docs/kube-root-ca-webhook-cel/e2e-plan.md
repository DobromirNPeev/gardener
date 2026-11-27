# E2E plan (from scratch): CEL `matchConditions` remediation for `kube-root-ca.crt`

Starts from **nothing** — no KinD cluster, no Gardener, no shoot, no webhook. Brings the whole stack
up from zero, reproduces the rotation hang, then proves the CEL fix clears it with gardenlet running
the new code.

Goal: prove **gardenlet's code** (not a hand-applied YAML) detects a blocking `configmaps` webhook
and patches it with a name-scoped CEL `matchCondition` that unblocks `kube-root-ca.crt` rotation,
leaving every other ConfigMap guarded and `failurePolicy` untouched.

- Code under test: branch `fix/kube-root-ca-webhook-cel`
- Worktree: `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel`
- Everything is run FROM that worktree, so skaffold builds the CEL code.

---

## 0. Prerequisites

- Docker Desktop running (`open -a Docker`; confirm `docker info` shows a `Server:` section), 8 GB+ RAM.
- macOS / zsh. `make` uses repo-pinned tool binaries, so `kind`/`skaffold` need not be on `PATH`.
- The CEL worktree exists:
  ```bash
  cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-reviews
  git worktree add -b fix/kube-root-ca-webhook-cel ../gardener-kube-root-ca-cel <master-base>
  # (if not already created)
  ```

All commands below run from the CEL worktree:
```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel
```

---

## 1. Enable the webhook remediator in the gardenlet config

The remediator is OFF by default. Turn it on in `dev-setup/gardenlet/base/gardenlet.yaml` under
`spec.config.controllers`:
```yaml
      shootCare:
        webhookRemediatorEnabled: true
```

---

## 2. Bring up KinD + Gardener from zero

```bash
make kind-up
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
make gardener-up      # operator-up + garden-up + seed-up — builds gardenlet from the CEL code
```
Slow: ~20–40+ min on a cold machine (image builds + control plane).

Because this is `make gardener-up` from the CEL worktree, the `local-skaffold/gardenlet` image is
built from our changed packages (`pkg/gardenlet/controller/shoot/care`,
`pkg/gardenlet/operation/botanist/matchers`), with `webhookRemediatorEnabled: true` baked into the
mounted config.

Verify the stack:
```bash
kubectl -n garden get pods | grep -E "gardenlet|gardener-operator|gardener-apiserver"
kubectl -n garden get configmap -o name | grep gardenlet     # find the gardenlet configmap
kubectl -n garden get configmap <gardenlet-configmap> -o yaml | grep webhookRemediatorEnabled   # true
```

---

## 3. Create a normal shoot and wait until Ready

A normal (not workerless, not hibernated) shoot — the wait task is skipped otherwise.

```bash
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/virtual-garden/kubeconfig
kubectl apply -f example/provider-local/shoot.yaml     # Shoot local / ns garden-local

NAMESPACE=garden-local ./hack/usage/wait-for.sh shoot local \
  APIServerAvailable ControlPlaneHealthy ObservabilityComponentsHealthy \
  EveryNodeReady SystemComponentsHealthy
```
Version-less example shoot defaults to ~1.37 (>= 1.33), so `matchConditions` is fully supported.

---

## 4. Plant the blocking webhook inside the shoot

```bash
./hack/usage/generate-kubeconfig.sh > admin-kubeconf.yaml     # shoot admin kubeconfig, 1h
```

Save `blocking-webhook.yaml` (configmaps CREATE/UPDATE, fail-closed, no backing pods):
```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: block-kube-root-ca
webhooks:
  - name: block-kube-root-ca.example.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    failurePolicy: Fail
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["configmaps"]
        scope: "*"
    clientConfig:
      service:
        name: nonexistent-webhook
        namespace: default
        path: /validate
        port: 443
```
```bash
kubectl --kubeconfig admin-kubeconf.yaml apply -f blocking-webhook.yaml
```

Confirm it blocks (baseline):
```bash
kubectl --kubeconfig admin-kubeconf.yaml -n default create configmap probe-before --from-literal=x=y
# expect: denied by block-kube-root-ca.example.com (service "nonexistent-webhook" not found)
```

---

## 5. Trigger CA rotation

```bash
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/virtual-garden/kubeconfig
kubectl -n garden-local annotate shoot local gardener.cloud/operation=rotate-ca-start
```

---

## 6. Observe BOTH loops race — remediation vs. the wait task

Because the remediator is ON, two things happen concurrently now:

```bash
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
GL=$(kubectl -n garden get pods -o name | grep gardenlet | head -1)

# the care loop should remediate the webhook within a cycle (syncPeriod ~30s):
kubectl -n garden logs -f $GL | grep -iE "Remediat|matchConditions|kube-root-ca|not yet confirmed"
```

Expect to see the remediation fire:
```
msg:"Remediating" ... webhookConfigName:"block-kube-root-ca" ... fieldName:"matchConditions"
```

---

## 7. THE PROOF — gardenlet patched the webhook itself

```bash
# (7a) CEL matchCondition written by gardenlet:
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.webhooks[0].matchConditions}'
# expect: name "gardener-exclude-kube-root-ca.crt" with
#   !(request.resource.group == "" && request.resource.resource == "configmaps"
#     && object != null && has(object.metadata.name) && object.metadata.name == "kube-root-ca.crt")

# (7b) failurePolicy untouched:
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.webhooks[0].failurePolicy}'            # expect: Fail

# (7c) warning annotation set:
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.metadata.annotations.gardener\.cloud/warning}'

# (7d) idempotent — resourceVersion stable across samples (no re-patch each cycle):
for i in 1 2 3; do
  kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
    -o jsonpath='{.metadata.resourceVersion}'; echo; sleep 4
done
```

---

## 8. Behavior checks — name-scoped, not fail-open

```bash
# (8a) other configmaps STILL blocked:
kubectl --kubeconfig admin-kubeconf.yaml -n default create configmap probe-after --from-literal=x=y
# expect: STILL denied

# (8b) kube-root-ca.crt now writable:
kubectl --kubeconfig admin-kubeconf.yaml -n default annotate configmap kube-root-ca.crt test=now --overwrite
# expect: annotated (succeeds)
```

---

## 9. Confirm the hang clears and rotation advances

```bash
# per-namespace: new 2-cert [old,new] bundle + confirmation label present
for ns in default kube-system kube-public kube-node-lease; do
  echo -n "$ns certs="; kubectl --kubeconfig admin-kubeconf.yaml -n $ns get configmap kube-root-ca.crt \
    -o jsonpath='{.data.ca\.crt}' | grep -c "BEGIN CERTIFICATE"
  echo -n "$ns confirmed="; kubectl --kubeconfig admin-kubeconf.yaml -n $ns get configmap kube-root-ca.crt \
    -o jsonpath='{.metadata.labels.credentials\.gardener\.cloud/ca-bundle-name}'; echo
done
# expect: all 4 → 2 certs + a ca-bundle-* label

# the wait task stops looping / Succeeds, reconcile progresses:
kubectl -n garden logs $GL --tail=30 | grep -iE "kube-root-ca|Succeeded"
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/virtual-garden/kubeconfig
kubectl -n garden-local get shoot local \
  -o jsonpath='{.status.credentials.rotation.certificateAuthorities.phase}{"\n"}'
```

If KCM's publisher hasn't re-pushed the bundle to every namespace yet (it only publishes on a
trigger), force it:
```bash
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
kubectl -n shoot--local--local rollout restart deploy/kube-controller-manager
```

---

## 10. Cleanup / teardown

```bash
# remove the planted webhook (and its gardenlet-added matchCondition):
kubectl --kubeconfig admin-kubeconf.yaml delete -f blocking-webhook.yaml
kubectl --kubeconfig admin-kubeconf.yaml -n default delete configmap probe-before probe-after --ignore-not-found

# tear everything down:
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
make gardener-down
make kind-down
```

---

## Pass criteria

| # | Expectation |
|---|---|
| 7a | gardenlet wrote matchCondition `gardener-exclude-kube-root-ca.crt` with the null-safe CEL |
| 7b | `failurePolicy` still `Fail` |
| 7c | `gardener.cloud/warning` annotation present |
| 7d | resourceVersion stable (idempotent) |
| 8a | other configmaps still denied by the webhook |
| 8b | `kube-root-ca.crt` writable |
| 9  | all namespaces get the new bundle + confirmation label; wait task Succeeds; rotation leaves Preparing |

All seven ⇒ the production code produces the remediation and it unblocks the rotation hang, live,
end-to-end — starting from an empty machine.
