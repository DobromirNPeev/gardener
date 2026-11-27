# Option 1 — Live KinD + Gardener e2e RESULT

**Date:** 2026-11-26
**Worktree:** `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-opt1-kubesystem`
**Branch:** `fix/kube-root-ca-webhook-opt1-kubesystem`
**Verdict:** PASS — CA rotation reached `phase: Prepared` end-to-end despite a blocking
`configmaps` webhook in a customer namespace. The fix (narrowing
`WaitUntilKubeRootCAConfigMapsUpdated` to `kube-system` only) behaves exactly as claimed.

## What was verified

Option 1 narrows `WaitUntilKubeRootCAConfigMapsUpdated`
(`pkg/gardenlet/operation/botanist/kubecontrollermanager.go`) by adding
`client.InNamespace(metav1.NamespaceSystem)` to the ConfigMap list, so gardenlet waits/labels
ONLY `kube-system/kube-root-ca.crt`. Claim: a customer webhook that blocks `kube-root-ca.crt`
writes in a non-kube-system namespace no longer hangs CA rotation.

## Environment bring-up

- Fresh cluster from this worktree: `make kind-up` then `make gardener-up` (KUBECONFIG passed
  inline through `nohup env`). Both succeeded.
- `seed/local`: Ready, `Reconcile Succeeded (100%)`.
- gardenlet pod: `gardenlet-8f6c48d57-qncqw` Running in `garden` namespace (built from this
  worktree, so the narrowed wait is the code under test).
- Shoot `local` created from `example/provider-local/shoot.yaml`, reconciled to
  `Create Succeeded (100%)`.
- Shoot admin kubeconfig generated via `hack/usage/generate-kubeconfig.sh` (file:
  `admin-kubeconf.yaml`, gitignored, not committed).
- Pre-existing `kube-root-ca.crt` ConfigMaps confirmed in: `default`, `kube-node-lease`,
  `kube-public`, `kube-system`.

## The webhook that was planted (faithful Option 1 isolation)

A `ValidatingWebhookConfiguration` on `configmaps` CREATE/UPDATE, `failurePolicy: Fail`, pointing
at a Service with no backing pods (so every intercepted write is rejected). Crucially, the
`namespaceSelector` EXCLUDES `kube-system` so the webhook blocks ONLY customer namespaces — this
isolates Option 1's behavior (kube-system writes succeed; customer-namespace writes are blocked).

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: block-configmaps-non-kube-system
webhooks:
- name: block-configmaps.example.com
  admissionReviewVersions: ["v1"]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 10
  namespaceSelector:
    matchExpressions:
    - key: kubernetes.io/metadata.name
      operator: NotIn
      values: ["kube-system"]      # block everywhere EXCEPT kube-system
  rules:
  - apiGroups: [""]
    apiVersions: ["v1"]
    operations: ["CREATE","UPDATE"]
    resources: ["configmaps"]
    scope: "*"
  clientConfig:
    service:
      namespace: default
      name: nonexistent-webhook
      path: /validate
      port: 443
```

Sanity checks (before rotation):
- `kubectl -n default create configmap probe` → **DENIED**:
  `failed calling webhook "block-configmaps.example.com": ... service "nonexistent-webhook" not found`
- `kubectl -n kube-system create configmap probe` → **SUCCESS** (then deleted).

## Rotation trigger

`kubectl -n garden-local annotate shoot local gardener.cloud/operation=rotate-ca-start --overwrite`
→ phase went `Preparing` immediately. `lastInitiationTime=2026-11-26T14:50:13Z`.

## Observed progression (phase / lastOperation)

| progress | description |
|----------|-------------|
| 20% | Waiting until main and event ETCDs have been reconciled (etcd restart for new CA) |
| 22% | Waiting until Kubernetes API server rolled out |
| 26% | Waiting until gardener-resource-manager reports readiness |
| 50% | Waiting until kube-controller-manager reports readiness |
| 74-76% | **Waiting until kube-root-ca.crt ConfigMaps have been updated with new CA bundle** |
| 80% | Waiting until worker pools have been reconciled (both nodes rolled with new CA) |
| 97% | Waiting until shoot Prometheus is reconciled |
| 100% | **lastOperation Succeeded — phase `Prepared`** |

Final: `phase=Prepared  lastOp=Succeeded:100%:Shoot cluster has been successfully reconciled.`

Note: during the kube-root-ca step the `kube-system` ConfigMap had not yet received the new bundle
from KCM's root-ca-cert-publisher; a `kubectl -n shoot--local--local rollout restart
deploy/kube-controller-manager` (documented gotcha) re-triggered the publisher and the
`kube-system` CM was then labeled within seconds, after which the flow advanced past 76%. This is a
local-timing nuance of the publisher, not related to Option 1.

## gardenlet log evidence (the crux)

Only ONE ConfigMap was ever considered by the wait (i.e. kube-system only — on `master` this list
would also include the blocked `default` CM and the wait would loop/hang):

```
14:52:57 "Found kube-root-ca.crt ConfigMaps not yet confirmed to contain the new CA bundle" number:1
14:53:27 "Found kube-root-ca.crt ConfigMaps not yet confirmed to contain the new CA bundle" number:1
14:53:27 "Succeeded" ... task:"Waiting until kube-root-ca.crt ConfigMaps have been updated with new CA bundle"
```

- Count of `"do not yet contain the expected CA bundle"` errors in gardenlet: **0**.
- kube-root-ca log lines referencing `default` / `kube-public` / `kube-node-lease`: **0**.

So gardenlet never attempted to write/wait on the blocked customer ConfigMaps — the blocking
webhook was never on its path.

## Final ConfigMap states (the documented tradeoff)

| namespace | `credentials.gardener.cloud/ca-bundle-name` label |
|-----------|---------------------------------------------------|
| `kube-system` | `ca-bundle-a2604ccc` (waited on, confirmed, labeled) |
| `default` | *(none — stale, untouched)* |
| `kube-public` | *(none — stale, untouched)* |
| `kube-node-lease` | *(none — stale, untouched)* |

After rotation, the webhook still blocks `default` configmap writes (re-tested: DENIED). The
customer-namespace trust bundles remain stale — exactly the deliberate Option 1 cost: rotation
completes instead of hanging, at the price of customer-namespace `kube-root-ca.crt` lagging until
the webhook is fixed / kubelet refreshes them.

## Conclusion

Option 1 is proven live: with a customer `configmaps` webhook blocking non-kube-system namespaces,
CA rotation reaches `Prepared` (does not hang). gardenlet waits on and labels only
`kube-system/kube-root-ca.crt`; customer-namespace ConfigMaps are left stale by design.

## Cleanup (NOT performed automatically; cluster left up for inspection)

```bash
kubectl --kubeconfig admin-kubeconf.yaml delete validatingwebhookconfiguration block-configmaps-non-kube-system
kubectl --kubeconfig dev-setup/kubeconfigs/virtual-garden/kubeconfig -n garden-local annotate shoot local gardener.cloud/operation=rotate-ca-complete --overwrite
make gardener-down
make kind-down
```
