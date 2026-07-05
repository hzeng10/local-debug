# Integration harness (kind + Istio ambient)

End-to-end regression test for `ldbg`: stands up a disposable **kind** cluster + **Istio
ambient** + the sample app, drives the real **`ldbg up` → `test` → `down`** flow, and
asserts the behaviors that are easy to break in a refactor but only observable on a real
ambient cluster:

- the **ambient opt-out** (`istio.io/dataplane-mode=none`) is applied on `up` and reverted on `down`
- an **in-cluster** request to the service lands on the **local** process (global takeover)
- the local process reaches the in-cluster **dependency** via telepresence
- the **offline** traffic-manager install path (`ldbg bundle` + `cluster install --import-via kind`)
- `down` removes the agent **before** reverting ambient, leaving the workload **pristine**
- the **log pipeline**: the log-analysis stack (VictoriaLogs + Vector, collect-all overlay)
  collects an **unlabeled** service, excludes `kube-system`, `ldbg logs query` works through
  the tunnel AND via the **port-forward fallback** while disconnected, `ldbg logs local`
  parses a Spring-format file, `up` injects `LOGGING_FILE_NAME`, and `doctor` reports the
  log-store/log-collection checks

## Files

- `harness.sh` — the orchestrator (setup → assert → teardown, with an EXIT trap)
- `manifests.yaml` — sample `orders` + `dep` in an ambient `demo` namespace
- `handler.py` — the local stand-in for the intercepted service (LOCAL-LAPTOP marker + `/call-dep`)
- `../../.github/workflows/integration.yml` — runs this on CI (ubuntu-latest)

## Run locally (throwaway machine only)

Needs: `docker`, `kind`, `kubectl`, `istioctl`, `go`, `python3`, and **passwordless sudo**
(the telepresence root daemon). It creates/deletes a kind cluster named `kind`.

```bash
test/integration/harness.sh          # full run + teardown
KEEP=1 test/integration/harness.sh   # keep the cluster up for debugging
TELEPRESENCE_VERSION=2.29.0 test/integration/harness.sh
LOG_ANALYSIS_REF=main test/integration/harness.sh   # pin the log-analysis ref (cloned into .bin/)
```

> ⚠️ The harness installs the telepresence client to `/usr/local/bin` and runs
> `telepresence connect` (root daemon). Don't run it on a workstation you care about —
> it's built for ephemeral CI runners. The `EXIT` trap deletes the kind cluster and
> stops the daemons (skip with `KEEP=1`).

## CI

`integration.yml` runs on `workflow_dispatch` and on PRs touching the Go code or this
harness. ubuntu-latest provides docker + passwordless sudo; the workflow installs
kubectl, kind, and istioctl, then runs `harness.sh`.
