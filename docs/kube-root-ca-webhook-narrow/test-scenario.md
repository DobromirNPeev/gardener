# Unit test scenarios — kube-root-ca.crt webhook narrow fix (Option 4)

This document lists the runnable unit tests that cover the "narrow" fix for the credential-rotation
hang caused by customer webhooks intercepting writes to the `kube-root-ca.crt` ConfigMap.

## What is under test (the three moving parts)

1. **GRM labeler controller** `pkg/resourcemanager/controller/rootcacertlabeler/` — a new
   gardener-resource-manager controller that stamps a stable label
   `config.gardener.cloud/kube-root-ca=true` (constant `v1beta1constants.LabelKeyKubeRootCACrt`) on
   every `kube-root-ca.crt` ConfigMap in the shoot.
2. **Care-loop remediation matcher** `pkg/gardenlet/operation/botanist/matchers/matcher.go` — a new
   matcher entry
   `{GVR: configmaps, ObjectLabels: labels.Set{config.gardener.cloud/kube-root-ca: "true"}}`
   so the webhook remediator adds an `objectSelector: config.gardener.cloud/kube-root-ca NotIn [true]`
   to any blocking configmaps webhook, excluding `kube-root-ca.crt` by label.
3. **Config plumbing** `pkg/apis/config/resourcemanager/v1alpha1/{types,defaults}.go`,
   `pkg/resourcemanager/controller/add.go`, and
   `pkg/component/gardener/resourcemanager/resource_manager.go` — the new
   `RootCACertLabelerControllerConfig` (fields `Enabled`, `ConcurrentSyncs`), its defaulting, its
   registration in the GRM controller manager, and its enablement for the `ForShootOrVirtualGarden`
   responsibility mode.

---

## 1. Labeler reconciler tests

**Package:** `pkg/resourcemanager/controller/rootcacertlabeler`
**File:** `reconciler_test.go` (Ginkgo/Gomega, fake client)

### Run

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
go test -count=1 -v ./pkg/resourcemanager/controller/rootcacertlabeler/...
```

### Cases asserted

| Spec (It) | Asserts |
| --- | --- |
| should add the label to a kube-root-ca.crt ConfigMap | After reconcile, the `kube-root-ca.crt` ConfigMap carries `config.gardener.cloud/kube-root-ca=true`. |
| should preserve existing labels when adding the label | The label is added **without** clobbering pre-existing labels (MergeFrom patch, not overwrite). |
| should not add the label to an unrelated ConfigMap | A ConfigMap named `some-other-configmap` is **not** labeled — the name guard in `Reconcile` holds even if a request for it reaches the queue. |
| should be a no-op for an already labeled kube-root-ca.crt ConfigMap | ResourceVersion is unchanged — no redundant patch when the label is already `true`. |
| should not fail if the ConfigMap is gone | A reconcile request for a missing object returns no error (NotFound swallowed). |

### Expected output

```
Ran 5 of 5 Specs in <…> seconds
SUCCESS! -- 5 Passed | 0 Failed | 0 Pending | 0 Skipped
ok  github.com/gardener/gardener/pkg/resourcemanager/controller/rootcacertlabeler
```

> Note: the reconciler's name guard (`configMap.Name != ConfigMapName`) is covered even though the
> `AddToManager` predicate (`ConfigMapPredicate`) already filters watch events to that name. The guard
> is the second line of defense for requests already in the queue, and the "unrelated ConfigMap" case
> proves it. There is no separate `add_test.go` for the predicate because the reconciler-level guard
> is the authoritative behavior; a predicate-only test would duplicate controller-runtime's own
> coverage and is intentionally not added.

---

## 2. Care-loop remediation tests (matcher + webhook remediator)

**Package:** `pkg/gardenlet/controller/shoot/care`

### 2a. Webhook remediation — objectSelector is added

**File:** `webhook_remediation_test.go`

New spec: **`objectSelector for kube-root-ca.crt ConfigMaps`**. It installs a `ValidatingWebhook`
with `failurePolicy: Fail` matching `configmaps` on `CREATE`, overrides `WebhookConstraintMatchers`
with the single kube-root-ca matcher, runs `remediator.Remediate(ctx)`, and asserts:

- the webhook gets the `gardener.cloud/warning` annotation (remediation happened),
- `failurePolicy` is left untouched as `Fail`,
- `webhook.ObjectSelector` becomes exactly:
  ```yaml
  matchExpressions:
  - key: config.gardener.cloud/kube-root-ca
    operator: NotIn
    values: ["true"]
  ```

This proves the remediator derives the `NotIn [true]` objectSelector from the new matcher's
`ObjectLabels`, i.e. `kube-root-ca.crt` is excluded from the customer webhook.

The existing `BeforeEach` fixture webhook in this file was also extended with the same
`ObjectSelector`, confirming that a webhook which already excludes the label is treated as
already-remediated (not flagged again).

### 2b. Constraints — a configmaps webhook that only excludes kube-system is still problematic

**File:** `constraints_test.go`

The configmaps GVR row was switched from `kubeSystemNamespaceTables` to a new
`configMapsTestTables`. Because `kube-root-ca.crt` ConfigMaps are protected in **all** namespaces
(not just kube-system), a webhook whose `namespaceSelector` only excludes `kube-system` is still
problematic. The one webhook considered **not** problematic is the one that excludes kube-system
**and** carries the `config.gardener.cloud/kube-root-ca NotIn [true]` objectSelector. This asserts
the constraint evaluation treats the label-scoped matcher as cluster-wide.

### Run

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
go test -count=1 ./pkg/gardenlet/controller/shoot/care/...
```

### Expected output

```
Ran 1418 of 1418 Specs in <…> seconds
SUCCESS! -- 1418 Passed | 0 Failed | 0 Pending | 0 Skipped
ok  github.com/gardener/gardener/pkg/gardenlet/controller/shoot/care
```

---

## 3. Config-plumbing tests

### 3a. GRM component wires the labeler config

**Package:** `pkg/component/gardener/resourcemanager`
**File:** `resource_manager_test.go`

The config-ConfigMap assertion for the `ForShootOrVirtualGarden` responsibility mode now expects
`config.Controllers.RootCACertLabeler = { Enabled: true }`, mirroring the production line in
`resource_manager.go` that enables the labeler for shoot/virtual-garden GRMs. This proves the
controller is turned on exactly where `kube-root-ca.crt` lives.

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
go test -count=1 ./pkg/component/gardener/resourcemanager/
```

Expected:

```
Ran 31 of 31 Specs in <…> seconds
SUCCESS! -- 31 Passed | 0 Failed | 0 Pending | 0 Skipped
ok  github.com/gardener/gardener/pkg/component/gardener/resourcemanager
```

### 3b. Defaulting of RootCACertLabelerControllerConfig

**Package:** `pkg/apis/config/resourcemanager/v1alpha1`

`SetDefaults_RootCACertLabelerControllerConfig` defaults `ConcurrentSyncs` to 5 only when the
controller is `Enabled`. This is exercised through the package's generated defaulting + roundtrip
tests.

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
go test -count=1 ./pkg/apis/config/resourcemanager/v1alpha1/...
```

Expected: `ok  github.com/gardener/gardener/pkg/apis/config/resourcemanager/v1alpha1`

---

## One-shot command for the whole change

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-narrow
go vet ./pkg/resourcemanager/controller/rootcacertlabeler/... \
       ./pkg/gardenlet/controller/shoot/care/... \
       ./pkg/gardenlet/operation/botanist/matchers/... \
       ./pkg/component/gardener/resourcemanager/... \
       ./pkg/apis/config/resourcemanager/v1alpha1/...

go test -count=1 ./pkg/resourcemanager/controller/rootcacertlabeler/... \
                 ./pkg/gardenlet/controller/shoot/care/... \
                 ./pkg/component/gardener/resourcemanager/... \
                 ./pkg/apis/config/resourcemanager/v1alpha1/...
```

All packages are expected to print `ok`. `pkg/gardenlet/operation/botanist/matchers` has no test
files of its own; its matcher is exercised through the care-loop `constraints_test.go` and
`webhook_remediation_test.go` above.
