# Option 1 — Local unit test scenario (kube-system-only `kube-root-ca.crt`)

## What Option 1 changes

`Botanist.WaitUntilKubeRootCAConfigMapsUpdated`
(`pkg/gardenlet/operation/botanist/kubecontrollermanager.go`) used to wait for / label the
`kube-root-ca.crt` ConfigMap in **every** namespace of the shoot cluster. Option 1 narrows the List
to `client.InNamespace(metav1.NamespaceSystem)`, so gardenlet now only waits on and labels the
`kube-system/kube-root-ca.crt` ConfigMap. `kube-root-ca.crt` ConfigMaps in customer namespaces are
deliberately ignored, so a customer admission webhook that blocks writes to those ConfigMaps can no
longer hang CA rotation.

## Covering unit tests

All in `pkg/gardenlet/operation/botanist/kubecontrollermanager_test.go`, under
`Describe("#WaitUntilKubeRootCAConfigMapsUpdated")`:

| `It` spec | Asserts |
|-----------|---------|
| should succeed immediately when there are no ConfigMaps | empty list → no error (unchanged). |
| should succeed immediately when all kube-root-ca.crt ConfigMaps are already labeled | already-labeled `kube-system` ConfigMap is skipped by the label selector → no error. |
| should label the kube-system ConfigMap that contains the expected bundle | the `kube-system` ConfigMap holding the new bundle gets the `credentials.gardener.cloud/ca-bundle-name` label. |
| should return an error when the kube-system ConfigMap has no ca.crt data | `kube-system` ConfigMap missing `ca.crt` → `1 kube-root-ca.crt ConfigMap(s) do not yet contain the expected CA bundle`. |
| should return an error when the kube-system ConfigMap does not contain the expected bundle | `kube-system` ConfigMap with stale `ca.crt` → same error. |
| should only process the unlabeled kube-system ConfigMap and ignore non-kube-root-ca.crt ConfigMaps | a differently-named ConfigMap in `kube-system` is never labeled; the already-labeled `kube-root-ca.crt` stays labeled. |
| **should ignore kube-root-ca.crt ConfigMaps in non-kube-system namespaces** | **crux of Option 1**: with a good `kube-system/kube-root-ca.crt` plus stale `default/kube-root-ca.crt` and `customer-ns/kube-root-ca.crt`, the call **succeeds**; the two non-kube-system ConfigMaps are left untouched (no label, data unchanged); only `kube-system` is labeled. Proves a stale customer-namespace ConfigMap neither blocks nor errors rotation. |

## Commands to run

From the worktree root
(`.../gardener-kube-root-ca-opt1-kubesystem`):

```bash
# Run only the relevant Describe (fast):
go test ./pkg/gardenlet/operation/botanist/ -run TestBotanist -count=1 \
  -args -ginkgo.focus="#WaitUntilKubeRootCAConfigMapsUpdated"

# Or the whole package (what CI runs):
go test ./pkg/gardenlet/operation/botanist/... -count=1

# Static analysis:
go vet ./pkg/gardenlet/operation/botanist/...
```

## Expected output

Focused run (7 specs):

```
Ran 7 of 652 Specs in ...
SUCCESS! -- 7 Passed | 0 Failed | 0 Pending | 0 Skipped
PASS
ok  	github.com/gardener/gardener/pkg/gardenlet/operation/botanist	...
```

Whole-package run:

```
Ran 652 of 652 Specs in ~16s
SUCCESS! -- 652 Passed | 0 Failed | 0 Pending | 0 Skipped
ok  	github.com/gardener/gardener/pkg/gardenlet/operation/botanist	...
```

`go vet` prints nothing and exits 0.
