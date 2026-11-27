# Option 1 (kube-system-only wait) — LIVE e2e: "no warning about skipped customer namespaces"

Date: 2026-11-27
Worktree: `gardener-kube-root-ca-opt1-kubesystem`
Branch: `fix/kube-root-ca-webhook-opt1-kubesystem`
Cluster: fresh KinD `gardener-local`, brought up FROM THIS worktree (teardown of the prior
Option 4 / hung-rotation cluster, then `make kind-up` + `make gardener-up`), so Option 1's
gardenlet code is what ran. Cluster LEFT UP. Nothing committed.

## Result summary

- Rotation reached **Prepared**: **YES** (lastOperation `Succeeded (100%)`, lastErrors none —
  Option 1 did NOT hang on the blocking customer webhook).
- kube-system `kube-root-ca.crt`: **UPDATED** — labeled `credentials.gardener.cloud/ca-bundle-name=ca-bundle-221dd269`,
  contains the **new 2-cert bundle**.
- default / kube-public / kube-node-lease `kube-root-ca.crt`: **STALE** — **unlabeled**, still 1 cert
  (writes blocked by the planted customer webhook; Option 1 deliberately does not wait on them).
- **Warning about the skipped customer namespaces: NONE found anywhere.** (The key deliverable.)

## Setup performed

1. Teardown of bad cluster: `kind delete cluster --name gardener-local` (completed).
2. `make kind-up` from this worktree (completed; cluster `gardener-local` created).
3. `export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig`; `make gardener-up`
   (completed; seed `local` Ready, `Reconcile Succeeded (100%)`; gardenlet pod Running).
4. Created shoot from `example/provider-local/shoot.yaml`; all 5 conditions True
   (APIServerAvailable, ControlPlaneHealthy, ObservabilityComponentsHealthy, EveryNodeReady,
   SystemComponentsHealthy).
5. Generated `admin-kubeconf.yaml` via `hack/usage/generate-kubeconfig.sh`.
6. Planted `ValidatingWebhookConfiguration/block-customer-configmaps`:
   failurePolicy=Fail, sideEffects=None, clientConfig.service=`nonexistent-webhook-svc` (no pods),
   rules: configmaps CREATE/UPDATE, scope `*`,
   namespaceSelector `kubernetes.io/metadata.name NotIn [kube-system]`.
   - Probe `-n default create configmap probe` -> **DENIED** ("service nonexistent-webhook-svc not found").
   - Probe `-n kube-system create configmap probe` -> **SUCCESS** (then deleted).
7. `kubectl -n garden-local annotate shoot local gardener.cloud/operation=rotate-ca-start` on virtual-garden.

## Post-rotation ConfigMap states (shoot side, via admin-kubeconf)

| namespace       | ca-bundle-name label             | certs in ca.crt | state   |
|-----------------|----------------------------------|-----------------|---------|
| kube-system     | `ca-bundle-221dd269`             | 2 (new bundle)  | UPDATED |
| default         | `<none>`                         | 1               | STALE   |
| kube-public     | `<none>`                         | 1               | STALE   |
| kube-node-lease | `<none>`                         | 1               | STALE   |

Pre-rotation baseline: all four ConfigMaps existed and were unlabeled.

## THE VERIFICATION — exhaustive search for any warning/event/condition/log. All came up EMPTY.

### (A) Shoot status conditions AND constraints + whole-YAML keyword grep
`KUBECONFIG=<virtual-garden> kubectl -n garden-local get shoot local -o yaml`
- Conditions: APIServerAvailable=True, ControlPlaneHealthy=True, EveryNodeReady=True,
  SystemComponentsHealthy=True; ObservabilityComponentsHealthy=Progressing — the Progressing one
  is unrelated Prometheus healthcheck noise, NOT about CA / kube-root-ca.
- Constraints: UsesUnifiedHTTPProxyPort=True; HibernationPossible=True ("All webhooks are properly
  configured."); MaintenancePreconditionsSatisfied=True ("All webhooks are properly configured.").
  Notably the webhook constraints do NOT even flag the planted blocking customer webhook.
- lastOperation: `Succeeded (100%) — Shoot cluster has been successfully reconciled.`
- lastErrors: none.
- grep of the full shoot YAML for `kube-root-ca|stale|outdated|skipped|not updated|customer namespace|ca-bundle`:
  **NO MATCHES.**

### (B) Kubernetes Events
- Garden side: `kubectl -n garden-local get events --field-selector involvedObject.name=local` ->
  only Normal `SchedulingSuccessful` / `Reconciling` / `Reconciled`. grep for CA keywords: **NO MATCHES.**
- Shoot side (all namespaces): `kubectl get events -A` grep
  `kube-root-ca|stale|outdated|skipped|not updated|ca-bundle|ca bundle`: **NO MATCHES.**
- Shoot side, events on the `kube-root-ca.crt` object in each of kube-system/default/kube-public/
  kube-node-lease: **No resources found** in every namespace.
- The only Warning-type events shoot-side are unrelated early-bringup node/daemonset noise
  (`UnscheduledNodeCriticalDaemonSets`, `UnreadyNodeCriticalPods`, kubelet node-IP), ~16m old,
  nothing about CA.

### (C) gardenlet logs
`kubectl -n garden logs <gardenlet-pod>` (pod `gardenlet-55889bbd57-f4nnn`).
- The ONLY log lines literally mentioning `kube-root-ca` are INFO-level:
  `"Found kube-root-ca.crt ConfigMaps not yet confirmed to contain the new CA bundle"` with
  **number=1** (i.e. only the single kube-system ConfigMap — never 4), followed by INFO `"Succeeded"`.
- Grep for any WARN/ERROR level line referencing
  `kube-root-ca|stale|customer|default|kube-public|kube-node-lease|ca-bundle|not updated|skipped`:
  **NONE.** (Other keyword hits in a broad grep were generic reconciliation noise such as
  "observed generation outdated" and "Deleting stale extension resources" — unrelated to CA.)

### (D) lastOperation / lastErrors
Already covered in (A): `Succeeded (100%)`, lastErrors none. No mention of the un-updated namespaces.

## Conclusion

Option 1 (kube-system-only wait) **completes CA rotation to Prepared without hanging** on a customer
admission webhook that blocks configmap writes in customer namespaces, and **leaves the
`kube-root-ca.crt` ConfigMaps in default/kube-public/kube-node-lease STALE (unlabeled, old single-cert
bundle) WITHOUT emitting any warning, event, condition, constraint, or WARN/ERROR log** that names
those namespaces or signals a stale/outdated CA. gardenlet only ever looked at kube-system (log
`number=1`). The customer would only discover the stale bundles when a pod in those namespaces fails
to reach the API server after `rotate-ca-complete`.
