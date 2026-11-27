# Option 4 (NARROW) — Live e2e result

Date: 2026-11-26
Worktree: `gardener-kube-root-ca-narrow` (branch `fix/kube-root-ca-webhook-narrow`)
Cluster: existing local KinD `gardener-local` (reused; redeployed Option 4 components).

## Verdict: FAIL on the decisive point (c)

- (a) Labeler stamps the label — **PARTIAL**: only 2 of 4 kube-root-ca.crt ConfigMaps got labeled.
- (b) Remediator adds `objectSelector NotIn` — **PASS**.
- (c) Rotation reaches `Prepared` while other configmaps stay blocked — **FAIL**: rotation HUNG at
  `Preparing` (progress ~76%), never reached `Prepared`.

## What was redeployed and how the new GRM reached the shoot

1. Set `spec.config.controllers.shootCare.webhookRemediatorEnabled: true` in
   `dev-setup/gardenlet/base/gardenlet.yaml`.
2. Copied the gitignored dev-setup artifacts (kubeconfigs + registry/TLS + patches) from the OPT2
   worktree so skaffold targeted the running cluster.
3. `make gardener-up` (operator-up + garden-up + seed-up) — rebuilt all images and rolled out the new
   gardenlet. seed-up alone updates the gardenlet, but the **shoot's** GRM is managed by gardenlet via
   a ManagedResource and was still running the OLD image.
4. **Forced a shoot reconcile** (`kubectl annotate shoot local gardener.cloud/operation=reconcile`) to
   roll the new GRM image (`...-dev-19b8fa29...`) + config into `shoot--local--local`. Both GRM pods
   then came up on the new image. This reconcile is the required extra step to get Option 4's GRM into
   the shoot control plane.
5. Confirmed `rootCACertLabeler.enabled: true` in the shoot GRM config
   (`cm/gardener-resource-manager-*`), and the controller started:
   `Starting Controller controller=root-ca-cert-labeler`, `worker count=5`.

## (a) Label state on all 4 kube-root-ca.crt ConfigMaps

| Namespace        | `config.gardener.cloud/kube-root-ca` |
|------------------|--------------------------------------|
| default          | *(absent)*                           |
| kube-system      | `true`                               |
| kube-public      | *(absent)*                           |
| kube-node-lease  | `true`                               |

Only `kube-system` and `kube-node-lease` were labeled, at labeler startup (15:39:44). `default` and
`kube-public` were NEVER reconciled by the labeler, even after several minutes.

### Root cause (genuine implementation gap, out of a test artifact)

The shoot GRM's **target cache is namespace-restricted**. Config shows
`targetClientConnection.namespaces: [kube-system, kubernetes-dashboard, kube-node-lease]`. Set at
`pkg/gardenlet/operation/botanist/resource_manager.go:62`:

```go
TargetNamespaces: []string{metav1.NamespaceSystem, v1beta1constants.KubernetesDashboardNamespace, corev1.NamespaceNodeLease},
```

The labeler (`pkg/resourcemanager/controller/rootcacertlabeler`) watches via
`targetCluster.GetCache()` and reads/patches via the cached `TargetClient`. Both are bounded by that
namespace list, so the labeler is **structurally incapable** of seeing or labeling
`kube-root-ca.crt` in `default` or `kube-public`. The Option 4 change did not extend `TargetNamespaces`
(nor give the labeler an uncached, cluster-wide client), so 2 of the 4 kube-root-ca.crt ConfigMaps can
never be labeled.

### Startup-window observation

The 2 reachable ConfigMaps were labeled within ~0.1s of the labeler starting (initial cache sync
enqueues existing objects that pass the name predicate). The label appears only after the new GRM pod
rolls out and the controller's cache syncs — so there is a window between GRM rollout and first
reconcile where even the reachable CMs are unlabeled.

## (b) Patched webhook (remediator) — PASS

Blocking webhook planted: `blocking-configmaps-webhook`, failurePolicy=Fail, no backing pods,
configmaps CREATE/UPDATE, scope `*`. It blocked a probe configmap create (`connection refused`).

Within ~45s the care-loop remediator patched it:

`webhooks[0].objectSelector`:
```json
{"matchExpressions":[{"key":"config.gardener.cloud/kube-root-ca","operator":"NotIn","values":["true"]}]}
```

`metadata.annotations["gardener.cloud/warning"]` (excerpt):
> ... The following modifications have been made:
> - objectSelector of webhook "block.configmaps.example.com" was extended with [{config.gardener.cloud/kube-root-ca NotIn [true]}]
> - namespaceSelector of webhook "block.configmaps.example.com" was extended with [{shoot.gardener.cloud/no-cleanup NotIn [true]} {gardener.cloud/purpose NotIn [kube-system]} {kubernetes.io/metadata.name NotIn [kube-system]}]

(The namespaceSelector additions come from the pre-existing kube-system configmaps matcher; they cover
`kube-system` only — NOT `default`/`kube-public`.)

## (c) CA rotation — FAIL (HUNG at Preparing)

Trigger: `kubectl annotate shoot local gardener.cloud/operation=rotate-ca-start`.

- Final phase: `certificateAuthorities.phase = Preparing` (never `Prepared`).
- `lastOperation`: stuck cycling around `Processing` 48–76% on the step
  "Waiting until kube-root-ca.crt ConfigMaps have been updated with new CA bundle".
- Gardenlet log, repeated every ~30s: `Found kube-root-ca.crt ConfigMaps not yet confirmed to contain
  the new CA bundle ... number=2` (dropped 4 → 2, then stuck at 2).

The 2 stuck ConfigMaps are `default` and `kube-public`. Their
`credentials.gardener.cloud/ca-bundle-name` label stayed on the OLD bundle (`ca-bundle-1f26db55`),
while `kube-system`/`kube-node-lease` advanced to the new bundle (`ca-bundle-e39ac650`).

### Mechanism (why it hangs)

`Botanist.WaitUntilKubeRootCAConfigMapsUpdated`
(`pkg/gardenlet/operation/botanist/kubecontrollermanager.go:90`) lists kube-root-ca.crt in **all**
namespaces of the shoot and PATCHes each to stamp the new `ca-bundle-name` label. That UPDATE is a
`configmaps UPDATE` and is intercepted by the customer webhook. For `kube-system`/`kube-node-lease` the
label exclusion lets the PATCH through; for the unlabeled `default`/`kube-public` the webhook DENIES the
PATCH, so gardenlet can never confirm them and the task loops forever.

### Direct proof of the label-scoped exclusion

- PATCH `default/kube-root-ca.crt` (unlabeled): **DENIED** (`connection refused` via the webhook).
- PATCH `kube-system/kube-root-ca.crt` (labeled): **ALLOWED**.
- Create `probe-after` configmap in `default` (non-root-ca): **DENIED**.

So the exclusion is correctly label-scoped and NOT fail-open — but because the labeler cannot reach
`default`/`kube-public`, rotation is blocked there.

## Conclusion / bug to flag

Option 4 as implemented does **not** unblock CA rotation end-to-end. The remediator and the matcher
work exactly as designed (label-scoped `NotIn` exclusion, warning annotation, negative control intact),
but the GRM `rootcacertlabeler` only covers the GRM target-cache namespaces
(`kube-system`, `kubernetes-dashboard`, `kube-node-lease`). The rotation confirmation step
(`WaitUntilKubeRootCAConfigMapsUpdated`) operates over **all** namespaces — including `default` and
`kube-public`, which always hold a `kube-root-ca.crt`. Those two never get labeled, the customer
webhook keeps denying their confirmation PATCH, and rotation hangs at `Preparing`.

To make Option 4 correct, the labeler must cover every namespace that holds a kube-root-ca.crt that
rotation touches — e.g. add `default` + `kube-public` (and generally all namespaces) to the shoot GRM
`TargetNamespaces`, or give the labeler a cluster-wide (uncached) client / an all-namespaces watch.

Cluster left UP. Nothing committed.
