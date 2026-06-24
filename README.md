# local-debug (`ldbg`)

Run a **Spring Boot** microservice on your **laptop** while it behaves as the live instance of a
service in a remote, shared Kubernetes cluster running **Istio in ambient mode** — it receives that
service's real traffic and calls the real in-cluster dependencies (DB, MQ, Redis, peer services),
debuggable in your IDE and drivable by **ClaudeCode**.

It replaces the slow loop (build jar → copy into a pod → restart → read logs → fix → repeat) with:
edit on the laptop → set a breakpoint → hit the service → step through real traffic.

`ldbg` is a thin wrapper around [Telepresence](https://telepresence.io) (its free, OSS **global/TCP
intercept** = full takeover) that adds Spring-Boot config sync, **Istio ambient handling**, an
air-gapped (offline) install path, and `--json` everywhere for AI agents.

> 中文文档：搭建与使用 [`docs/SETUP.zh-CN.md`](docs/SETUP.zh-CN.md)；
> Windows 11 → 远程气隙集群验证 Runbook [`docs/RUNBOOK.windows-remote.zh-CN.md`](docs/RUNBOOK.windows-remote.zh-CN.md)。

## How it works

```
in-cluster caller ──► <svc> Service ──►(global intercept)──► your laptop process ──►(telepresence)──► real deps
                                              ▲                                          │
                                         IDE breakpoints                            real DB/MQ/…
```

- **Inbound takeover** — all cluster traffic to the target Service routes to your local process
  (global/TCP intercept; no header routing, no waypoint, **no license** — air-gap friendly). This is a
  full takeover and disrupts other users of the shared service while active (accepted trade-off).
- **Outbound** — your local process reaches in-cluster dependencies by their cluster DNS names through
  the `telepresence connect` tunnel.
- **Config sync** — `ldbg` exports the workload's cluster env (`env`/`envFrom`/ConfigMap/Secret) to an
  env-file; Spring Boot *relaxed binding* applies it over `application.yaml` with **no app changes**.

## Istio ambient: the one thing that matters

On ambient, an intercepted workload that stays in the mesh gets its app port black-holed —
istio-cni's ztunnel redirection and the injected telepresence traffic-agent both claim the port, so
in-cluster callers see **"connection reset by peer"**. `ldbg up` handles this automatically: it
excludes the *intercepted* workload from ambient (`istio.io/dataplane-mode=none` on its pod template);
dependencies stay in the mesh. `ldbg down` reverts it. (Validated on minikube + Istio ambient 1.30 +
Telepresence 2.29.0.)

## Quickstart

```bash
# one-time per session: start the cluster network daemon (needs sudo/admin once)
telepresence connect

# bring the service onto your laptop (sync env + ambient opt-out + global intercept)
ldbg up orders -n demo

# run your Spring Boot app on the printed local port (IDE debug, bootRun, or java -jar)
#   IDE: point the run config's EnvFile at .ldbg/orders.env, debug on that port
#   or:  set -a; . .ldbg/orders.env; set +a; ./gradlew bootRun

ldbg test orders -n demo     # in-cluster request → proves it lands on your laptop
ldbg down                    # leave intercept, revert ambient, disconnect, clean up
```

## Commands

| Command | What it does |
| --- | --- |
| `ldbg up <svc> -n <ns>` | sync env → connect → ambient opt-out → global intercept (optionally `--run`) |
| `ldbg down` | leave intercepts, revert ambient opt-out, disconnect, remove generated files |
| `ldbg status [--json]` | telepresence/cluster/intercept state + a next-step hint |
| `ldbg doctor [svc] -n <ns>` | preflight: client, cluster, traffic-manager, ambient assessment |
| `ldbg sync <svc> -n <ns>` | only generate the env-file from the workload's cluster env |
| `ldbg test <svc> -n <ns>` | in-cluster probe that shows what answered (proves takeover) |
| `ldbg logs <svc> -n <ns>` | tail the workload's pods (incl. traffic-agent); `--manager`, `-f`, `--tail` |
| `ldbg intercept` / `leave` | low-level global intercept controls |
| `ldbg bundle` | (internet machine) `docker save` the traffic-manager image to a tarball |
| `ldbg cluster install` | (air-gapped) import the image + install traffic-manager (embedded chart) |

Every command supports `--json` and meaningful exit codes. `sync` and `up` accept
`--run-config intellij|vscode` to generate an IDE run config wired to the synced env-file
(IntelliJ `.run/*.run.xml` via the EnvFile plugin; VS Code `.vscode/launch.json` via the
native `envFile`).

## Offline / air-gapped install

The traffic-manager and injected traffic-agent are the **same image**:
`ghcr.io/telepresenceio/tel2:<version>`. The Helm chart is embedded in the Telepresence client (no
internet, no `helm` binary).

```bash
# on an internet-connected machine
ldbg bundle --tp-version 2.29.0 --out tel2-bundle.tar

# inside the air-gapped environment (image imported, then embedded-chart install, pullPolicy=IfNotPresent)
ldbg cluster install --bundle tel2-bundle.tar --import-via registry --registry <internal-registry/path>
#   or for minikube/kind/k3d:  --import-via minikube
```

## ClaudeCode usage

```bash
ldbg up orders -n demo --json      # structured result an agent can branch on
ldbg status --json                 # connected / interceptActive / clusterReachable / hint
ldbg test orders -n demo --json    # proves takeover from an in-cluster origin
```

Split of work: the developer drives IDE breakpoints; ClaudeCode runs `up`/`test`/`status`/`logs`,
reads stack traces, edits and iterates — both share the same `ldbg` session.

## Build

```bash
make build      # host binary ./ldbg
make cross      # dist/ldbg-{linux,windows,darwin}-…  (Windows 11 + Ubuntu are the target laptops)
make test       # unit tests
```

Requires Go 1.22+. Targets Telepresence 2.29.0 (see `ldbg version`).

**Integration test** — `test/integration/harness.sh` stands up kind + Istio ambient + the
sample app and drives the real `up`→`test`→`down` flow, asserting the ambient opt-out,
in-cluster takeover, outbound, offline install, and clean teardown. It runs in CI
(`.github/workflows/integration.yml`); see [`test/integration/README.md`](test/integration/README.md).

## Caveats / scope

- **Full takeover**: during an intercept, all shared-cluster traffic to the target hits your laptop
  (and fails if your app is down or paused on a breakpoint). The free global intercept is the only
  practical mode for an air-gapped cluster (header-based *personal* intercepts are paid and need a
  license the cluster can't fetch).
- **Outbound identity**: the laptop's calls egress via the traffic-agent in the intercepted pod, so a
  dependency sees the intercepted workload's pod IP as source — caller-keyed L4 policies keyed to that
  workload pass.
- **Out of scope (for now)**: header/personal intercepts, multiple services local at once, and non-JVM
  services.
