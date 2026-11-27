# E2E run log: CEL `matchConditions` remediation — commands actually executed

Live verification that **gardenlet's code** (not a hand-applied YAML) detects a blocking `configmaps`
webhook and patches it with a name-scoped CEL `matchCondition` that unblocks `kube-root-ca.crt`
rotation. Run on the local KinD cluster, 2026-11-25.

- Code under test: branch `fix/kube-root-ca-webhook-cel`, worktree
  `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel`
- Cluster + kubeconfigs: main worktree
  `/Users/I763496/SAPDevelop/go/src/github.com/gardener-reviews`
- gardenlet pod this run: `gardenlet-74b74f54fb-xskpq` (namespace `garden`)
- planted blocking webhook in shoot: `block-kube-root-ca`

---

## 1. Setup — CEL worktree prep

```bash
# (a) enable the remediator in the CEL worktree config — edited file, not a command:
#     dev-setup/gardenlet/base/gardenlet.yaml  →  under `controllers:`
#       shootCare:
#         webhookRemediatorEnabled: true

# (b) sync the 15 gitignored local-state files from main worktree into the CEL worktree
#     (kubeconfigs, TLS certs, config patch overlays — they exist only in the main checkout)
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-reviews
git ls-files --others --ignored --exclude-standard -- dev-setup/       # enumerate them
# then for each file f: cp "$MAIN/$f" "$CEL/$f"
#   dev-setup/kubeconfigs/{seed,virtual-garden,runtime}/kubeconfig
#   dev-setup/gardenconfig/components/credentials/secret-project-{garden,local}/kubeconfig/kubeconfig
#   dev-setup/gardenlet/base/{patch-helm-ref.yaml,patch-imagevector-overwrite.yaml,secret-registry-ca.yaml}
#   dev-setup/gardenlet/components/kubeconfigs/seed-local/kubeconfig
#   dev-setup/extensions/provider-local/components/extension/{patch-extension-prow.yaml,secret-registry-ca.yaml}
#   dev-setup/infra/registry/tls/{ca.crt,ca.key,registry.crt,registry.key}
```

Why (b) was needed: `make seed-up` from a fresh worktree fails because skaffold reads these
worktree-relative files, which are gitignored and present only where the cluster was brought up.

---

## 2. Build + redeploy gardenlet from the CEL code

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-cel
KC=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
nohup env KUBECONFIG=$KC make seed-up KUBECONFIG=$KC > /tmp/cel-seed-up.log 2>&1 &
# rebuilds local-skaffold/gardenlet from the CEL working tree and redeploys into KinD.
# NOTE: KUBECONFIG must be passed inline — `export` does not survive into the nohup subshell
#       (symptom when missing: instant "connection refused to localhost:8080", 0 images built).
```

Result: `make errors: 0`; seed/local reached `Succeeded`, post-deploy hooks completed.

---

## 3. Confirm gardenlet runs the new code + flag

```bash
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
kubectl -n garden get pods | grep gardenlet          # fresh pod, recent AGE
kubectl -n garden get deploy gardenlet -o jsonpath='{.spec.template.spec.containers[0].image}'
kubectl -n garden get configmap gardenlet-configmap-6e72111c -o yaml | grep webhookRemediatorEnabled
# -> webhookRemediatorEnabled: true (mounted via volume gardenlet-config)
```

---

## 4. Regenerate the shoot admin kubeconfig

```bash
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-reviews
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/virtual-garden/kubeconfig   # required, or gen fails
./hack/usage/generate-kubeconfig.sh > admin-kubeconf.yaml
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfigurations   # reachability
```

---

## 5. THE PROOF — gardenlet patched the webhook itself

```bash
# (5a) the CEL matchCondition gardenlet wrote:
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.webhooks[0].matchConditions}'
#   -> name: gardener-exclude-kube-root-ca.crt
#      expression: !(request.resource.group == "" && request.resource.resource == "configmaps"
#                    && object != null && has(object.metadata.name)
#                    && object.metadata.name == "kube-root-ca.crt")

# (5b) failurePolicy untouched:
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.webhooks[0].failurePolicy}'                      # -> Fail

# (5c) warning annotation set by gardenlet:
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.metadata.annotations.gardener\.cloud/warning}'   # -> "ATTENTION: ... modified by Gardener ..."

# (5d) gardenlet's own remediation log line:
kubectl -n garden logs gardenlet-74b74f54fb-xskpq --tail=300 | grep -iE "Remediat|matchConditions"
#   -> msg:"Remediating" ... webhookConfigName:"block-kube-root-ca" ... fieldName:"matchConditions"

# (5e) idempotent — resourceVersion stable across samples (no re-patching):
kubectl --kubeconfig admin-kubeconf.yaml get validatingwebhookconfiguration block-kube-root-ca \
  -o jsonpath='{.metadata.resourceVersion}'        # -> 27254, 27254, 27254
```

---

## 6. Behavior checks

```bash
# (6a) other configmaps STILL blocked (name-scoped, not fail-open):
kubectl --kubeconfig admin-kubeconf.yaml -n default create configmap probe-after --from-literal=x=y
#   -> denied by block-kube-root-ca.example.com (service "nonexistent-webhook" not found)

# (6b) kube-root-ca.crt now writable:
kubectl --kubeconfig admin-kubeconf.yaml -n default annotate configmap kube-root-ca.crt e2e-test=now --overwrite
#   -> configmap/kube-root-ca.crt annotated

# (6c) per-namespace bundle cert count + confirmation label:
for ns in default kube-system kube-public kube-node-lease; do
  kubectl --kubeconfig admin-kubeconf.yaml -n $ns get configmap kube-root-ca.crt \
    -o jsonpath='{.data.ca\.crt}' | grep -c "BEGIN CERTIFICATE"
  kubectl --kubeconfig admin-kubeconf.yaml -n $ns get configmap kube-root-ca.crt \
    -o jsonpath='{.metadata.labels.credentials\.gardener\.cloud/ca-bundle-name}'
done
```

---

## 7. Drive the hang to resolution

```bash
# (7a) nudge a reconcile so rotation re-enters the wait task:
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/virtual-garden/kubeconfig
kubectl -n garden-local annotate shoot local gardener.cloud/operation=reconcile --overwrite

# (7b) restart KCM so root-ca-cert-publisher re-pushes the new bundle to all namespaces
#      (writes now pass the remediated webhook):
export KUBECONFIG=$PWD/dev-setup/kubeconfigs/runtime/kubeconfig
kubectl -n shoot--local--local rollout restart deploy/kube-controller-manager

# (7c) observe:
kubectl -n garden logs gardenlet-74b74f54fb-xskpq --tail=25 | grep -oE '"number":[0-9]+'
kubectl -n garden-local get shoot local \
  -o jsonpath='{.status.credentials.rotation.certificateAuthorities.phase}'
kubectl -n garden-local get shoot local -o jsonpath='{.status.lastOperation.description}'
```

---

## Results

| Check | Before fix | After fix |
|---|---|---|
| webhook has CEL matchCondition | no | **yes** (`gardener-exclude-kube-root-ca.crt`, written by gardenlet) |
| failurePolicy | Fail | **Fail** (untouched) |
| warning annotation | none | **set** |
| re-patches each cycle | — | **no** (resourceVersion stable 27254) |
| other configmaps blocked | yes | **yes** (name-scoped, not fail-open) |
| kube-root-ca.crt writable | no | **yes** |
| all 4 ns have 2-cert `[old,new]` bundle | no (1 cert) | **yes (all 4)** |
| all 4 ns confirmed (`ca-bundle-name` label) | none | **all 4** (`ca-bundle-3116781f`) |
| `kube-root-ca` wait task | loops `number:4` forever | **Succeeded**, reconcile advanced to 80% |

The decisive chain: gardenlet wrote the CEL condition → the publisher's writes to `kube-root-ca.crt`
pass → all namespaces get the new bundle → gardenlet confirms them → the wait task that was pinned at
`number:4` for ~1h completes and the reconcile moves past it.

---

## Mutations made to the live cluster (reversible)

1. Redeployed gardenlet (`make seed-up`) — new pod, config with `webhookRemediatorEnabled: true`.
2. Annotated `default/kube-root-ca.crt` with `e2e-test=now`.
3. Attempted (rejected, nothing created) `default/probe-after` ConfigMap.
4. Annotated shoot `local` with `operation=reconcile`.
5. Restarted `kube-controller-manager` in `shoot--local--local`.
6. The planted `block-kube-root-ca` webhook now carries the gardenlet-written matchCondition +
   warning annotation (evidence of what gardenlet did).

Also left in the CEL worktree: `webhookRemediatorEnabled: true` in
`dev-setup/gardenlet/base/gardenlet.yaml`, and the 15 copied gitignored files. Revert the flag if you
don't want it persisted on the branch (the copied files are gitignored, so they won't be committed).

---

## What this proves vs. the hand-applied-YAML approach

Only this full e2e exercises the production code end-to-end: matcher detection
(`{configmaps, ObjectNames:["kube-root-ca.crt"]}`), the three-predicate routing
(`mustRemediateMatchConditions` fires; `mustRemediateFailurePolicy`/`mustRemediateSelectors` do not),
`matchConditions()`, `extendMatchConditions` (idempotent), and the live `Patch` — all inside a running
gardenlet. Combined with the green unit tests (1421/1421 + 3/3), both halves of the fix are verified.
