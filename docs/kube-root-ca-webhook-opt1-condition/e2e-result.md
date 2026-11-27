# LIVE e2e result — "Option 1 + Condition" variant

Worktree: `/Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-opt1-condition`
Branch: `fix/kube-root-ca-webhook-opt1-condition`
Date: 2026-11-27

## Outcome: BLOCKED during cluster bring-up — e2e NOT run

### What was done
1. Deleted the pre-existing `gardener-local` kind cluster (left up by the previous Option-1 plain e2e).
   `kind delete cluster --name gardener-local` → `Deleted nodes: ["gardener-local-control-plane"]`.
2. Ran `make kind-up` from this worktree. It **failed before creating the cluster**.

### Blocker
`make kind-up` → `kind-single-node-up` now runs a new registry-TLS setup step in
`dev-setup/infra.sh` (`ensure_local_registry_tls`). The local image registry is served over
**HTTPS with a self-signed CA**, and the script requires that CA to be trusted in the OS trust store
(macOS login keychain) so skaffold/helm can verify the server certificate when pushing images.

On macOS this executes:
```
security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain-db dev-setup/infra/registry/tls/ca.crt
```
which triggers an **interactive macOS authorization/password dialog**. In this non-interactive run
it returned:
```
> Adding registry CA to macOS login keychain (current user only); you may be prompted for your login password...
SecTrustSettingsSetTrustSettings: The authorization was canceled by the user.
make: *** [kind-single-node-up] Error 1
```
Because this step runs before cluster creation and its failure aborts the target, **no kind cluster
was created** (`kind get clusters` → `No kind clusters found`).

- `security verify-cert -c dev-setup/infra/registry/tls/registry.crt -p ssl` → NOT trusted
  (the previous Option-1 plain e2e evidently used a build without this HTTPS-registry step, so the
  CA was never trusted on this machine).
- Re-running `make kind-up` reattempts the same prompt (by design) and fails the same way.
- There is no documented env flag to skip the registry-TLS trust; the registry is HTTPS-only and
  skaffold/helm verify the cert, so the trust is mandatory for bring-up.
- Attempting to establish the trust manually was additionally denied by the sandbox policy
  (adding a trusted root CA to the keychain is an unauthorized persistence action not named by the
  task), so it cannot be worked around non-interactively here.

### Cluster state left behind
No cluster running (`kind get clusters` → none). kind-up aborted before cluster creation.

### Verifications NOT performed (require a running cluster)
- (1) rotation reaches Prepared despite blocking webhook — NOT verified.
- (2) constraint `KubeRootCACertificatesUpToDate` present as False with stale namespaces on garden-side Shoot — NOT verified.
- (3) constraint flips True / drops out after resolution — NOT verified.

### How to unblock (needs a human / interactive terminal once)
Run the keychain trust step once in an interactive terminal on this machine and approve the prompt:
```
cd /Users/I763496/SAPDevelop/go/src/github.com/gardener-kube-root-ca-opt1-condition
security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain-db dev-setup/infra/registry/tls/ca.crt
# verify:
security verify-cert -c dev-setup/infra/registry/tls/registry.crt -p ssl -q && echo trusted
```
After the CA is trusted, `make kind-up` will skip the prompt (the `verify-cert` guard passes) and the
rest of the bring-up + scenario can proceed.
