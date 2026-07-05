#!/usr/bin/env bash
# End-to-end integration harness: kind + Istio ambient + the sample app, driving the
# real ldbg up -> test -> down flow and asserting the hard-won behaviors don't regress
# (ambient opt-out apply/revert, in-cluster takeover, outbound via telepresence, clean
# teardown). Designed for CI (GitHub Actions ubuntu-latest, which has passwordless sudo
# for the telepresence root daemon). Run locally only on a throwaway machine.
#
# Usage:  test/integration/harness.sh
# Env:    TELEPRESENCE_VERSION (default 2.29.0), LOG_ANALYSIS_REF (default main),
#         KEEP=1 to skip teardown for debugging.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
TELEPRESENCE_VERSION="${TELEPRESENCE_VERSION:-2.29.0}"
LOG_ANALYSIS_REF="${LOG_ANALYSIS_REF:-main}"
LOG_ANALYSIS_REPO="https://github.com/hzeng10/log-analysis"
TEL_IMAGE="ghcr.io/telepresenceio/tel2:${TELEPRESENCE_VERSION}"
WHOAMI_IMAGE="traefik/whoami:v1.10.2"
CURL_IMAGE="curlimages/curl:8.10.1"
VLOGS_IMAGE="docker.io/victoriametrics/victoria-logs:v1.51.0"
VECTOR_IMAGE="docker.io/timberio/vector:0.56.0-distroless-libc"
BIN="$HERE/.bin"
LDBG="$BIN/ldbg"
TP="$BIN/telepresence"
HANDLER_PID=""

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m  ✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m  ✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  local rc=$?
  if [[ -n "$HANDLER_PID" ]]; then kill "$HANDLER_PID" 2>/dev/null || true; fi
  if [[ "${KEEP:-0}" != "1" ]]; then
    log "Teardown"
    "$TP" quit --stop-daemons 2>/dev/null || true
    kind delete cluster 2>/dev/null || true
  fi
  exit $rc
}
trap cleanup EXIT

require() { command -v "$1" >/dev/null 2>&1 || fail "missing required tool: $1"; }

log "Preflight"
for t in docker kind kubectl istioctl go python3 curl git; do require "$t"; done
mkdir -p "$BIN"

log "Install telepresence client v${TELEPRESENCE_VERSION}"
if [[ ! -x "$TP" ]]; then
  curl -fL "https://github.com/telepresenceio/telepresence/releases/download/v${TELEPRESENCE_VERSION}/telepresence-linux-amd64" -o "$TP"
  chmod +x "$TP"
fi
# telepresence starts its root daemon via sudo with an absolute path; make it resolvable.
sudo install -m 0755 "$TP" /usr/local/bin/telepresence
"$TP" version | head -1

log "Build ldbg"
( cd "$ROOT" && go build -o "$LDBG" . )
"$LDBG" version

log "Create kind cluster (default name 'kind')"
kind create cluster --wait 120s

log "Install Istio ambient"
istioctl install --set profile=ambient --skip-confirmation
kubectl -n istio-system rollout status ds/ztunnel --timeout=120s
kubectl -n istio-system rollout status ds/istio-cni-node --timeout=120s

log "Side-load images into kind (air-gap simulation)"
docker pull "$WHOAMI_IMAGE"; kind load docker-image "$WHOAMI_IMAGE"
docker pull "$CURL_IMAGE";   kind load docker-image "$CURL_IMAGE"

log "Offline-install traffic-manager via ldbg (bundle + import-via kind)"
( cd "$HERE" && "$LDBG" bundle --tp-version "$TELEPRESENCE_VERSION" --out tel2-bundle.tar )
"$LDBG" cluster install --bundle "$HERE/tel2-bundle.tar" --import-via kind
kubectl -n ambassador rollout status deploy/traffic-manager --timeout=120s
ok "traffic-manager installed from $TEL_IMAGE"

log "Deploy log stack (log-analysis @ ${LOG_ANALYSIS_REF}, collect-all overlay)"
if [[ ! -d "$BIN/log-analysis/.git" ]]; then
  git clone --depth 1 --branch "$LOG_ANALYSIS_REF" "$LOG_ANALYSIS_REPO" "$BIN/log-analysis"
else
  git -C "$BIN/log-analysis" fetch --depth 1 origin "$LOG_ANALYSIS_REF" && git -C "$BIN/log-analysis" checkout FETCH_HEAD
fi
docker pull "$VLOGS_IMAGE";  kind load docker-image "$VLOGS_IMAGE"
docker pull "$VECTOR_IMAGE"; kind load docker-image "$VECTOR_IMAGE"
kubectl apply -k "$BIN/log-analysis/deploy/overlays/collect-all/"
kubectl -n logging scale deploy grafana --replicas=0   # custom image not built in CI
kubectl -n logging rollout status statefulset/victorialogs --timeout=180s
kubectl -n logging rollout status ds/vector --timeout=180s
ok "log stack up (VictoriaLogs + Vector collect-all)"

log "Deploy sample app (ambient namespace)"
kubectl apply -f "$HERE/manifests.yaml"
kubectl -n demo rollout status deploy/dep --timeout=120s
kubectl -n demo rollout status deploy/orders --timeout=120s

log "Connect (root daemon needs sudo; CI is passwordless)"
"$TP" connect -n demo
"$TP" status | grep -q "Connected" || fail "telepresence did not connect"
ok "connected"

log "Start local handler on :8080"
DEP_URL="http://dep.demo.svc.cluster.local" SPRING_PROFILES_ACTIVE="cluster" PORT=8080 \
  python3 "$HERE/handler.py" &
HANDLER_PID=$!
for _ in $(seq 1 15); do curl -sf --max-time 2 http://localhost:8080/ >/dev/null 2>&1 && break; sleep 1; done
curl -sf --max-time 3 http://localhost:8080/ >/dev/null || fail "local handler did not come up"
ok "handler up (pid $HANDLER_PID)"

log "ldbg up (sync + ambient opt-out + global intercept)"
"$LDBG" up orders -n demo
dpm=$(kubectl -n demo get deploy orders -o jsonpath='{.spec.template.metadata.labels.istio\.io/dataplane-mode}')
ann=$(kubectl -n demo get deploy orders -o jsonpath='{.spec.template.metadata.annotations.ldbg\.local-debug/ambient-optout}')
[[ "$dpm" == "none" && "$ann" == "applied" ]] || fail "ambient opt-out not applied (dpm=$dpm ann=$ann)"
ok "ambient opt-out applied with ldbg annotation"
grep -q "LOGGING_FILE_NAME" .ldbg/orders.env || fail "synthetic LOGGING_FILE_NAME not injected into the env-file"
ok "local-log env injection present in env-file"

incluster() { # run curl from inside the cluster (real takeover origin)
  kubectl -n demo run "probe-$1" --image="$CURL_IMAGE" --restart=Never --rm -i --quiet -- \
    curl -s --max-time 12 "$2" 2>/dev/null
}

log "Assert INBOUND takeover (in-cluster request lands on the laptop)"
out_in="$(incluster in http://orders.demo.svc.cluster.local:8080/)"
echo "$out_in" | grep -q "LOCAL-LAPTOP" || fail "inbound did not reach the local handler: $out_in"
ok "inbound takeover works"

log "Assert OUTBOUND to cluster dependency"
out_dep="$(incluster dep http://orders.demo.svc.cluster.local:8080/call-dep)"
echo "$out_dep" | grep -q '"dependencyReachable": true' || fail "outbound to dep failed: $out_dep"
echo "$out_dep" | grep -q "CLUSTER-DEP" || fail "dep response not seen: $out_dep"
ok "outbound to cluster dependency works"

log "ldbg test (tool's own in-cluster assertion)"
"$LDBG" test orders -n demo --json | grep -q '"succeeded": true' || fail "ldbg test failed"
ok "ldbg test passed"

# jq-free count extractor for ldbg --json envelopes.
qcount() { python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["count"])'; }

log "Assert LOG PIPELINE (collect-all via tunnel)"
sleep 20   # give Vector time to ship the freshly-restarted pods' startup lines
n_dep=$("$LDBG" logs query dep --since 10m --json | qcount)
[[ "$n_dep" -ge 1 ]] || fail "unlabeled service 'dep' not collected (count=$n_dep) — collect-all broken"
ok "collect-all: unlabeled service collected (count=$n_dep)"
n_ks=$("$LDBG" logs query -n kube-system --since 10m --json | qcount)
[[ "$n_ks" -eq 0 ]] || fail "kube-system leaked into the log store (count=$n_ks) — namespace exclusion broken"
ok "namespace exclusion enforced (kube-system count=0)"

log "Assert ldbg logs local (fabricated Spring-format line)"
mkdir -p .ldbg/logs
printf '%s  INFO 1 --- [main] c.e.Harness : integration marker\n' "$(date '+%Y-%m-%d %H:%M:%S').000" > .ldbg/logs/orders.log
n_local=$("$LDBG" logs local orders --json | qcount)
[[ "$n_local" -eq 1 ]] || fail "logs local did not return the fabricated entry (count=$n_local)"
ok "logs local works"

log "Assert doctor log checks"
doctor_json=$("$LDBG" doctor orders -n demo --json || true)
echo "$doctor_json" | grep -q '"log-store"' || fail "doctor missing log-store check: $doctor_json"
echo "$doctor_json" | grep -q '"log-collection"' || fail "doctor missing log-collection check: $doctor_json"
ok "doctor log-store/log-collection checks present"

log "ldbg down (leave -> uninstall agent -> revert ambient -> disconnect)"
down_json="$("$LDBG" down -n demo --json)"
echo "$down_json" | grep -q '"uninstalledAgents"' || fail "down did not uninstall the agent: $down_json"
echo "$down_json" | grep -q '"revertedAmbient"' || fail "down did not revert ambient: $down_json"
kubectl -n demo rollout status deploy/orders --timeout=120s

log "Assert PRISTINE baseline after teardown"
dpm2=$(kubectl -n demo get deploy orders -o jsonpath='{.spec.template.metadata.labels.istio\.io/dataplane-mode}')
ann2=$(kubectl -n demo get deploy orders -o jsonpath='{.spec.template.metadata.annotations.ldbg\.local-debug/ambient-optout}')
[[ -z "$dpm2" && -z "$ann2" ]] || fail "workload not restored to baseline (dpm=$dpm2 ann=$ann2)"
ncont=$(kubectl -n demo get pod -l app=orders -o jsonpath='{.items[0].spec.containers[*].name}' | wc -w)
[[ "$ncont" == "1" ]] || fail "traffic-agent not removed (containers=$ncont)"
ok "orders restored to pristine baseline (in ambient, no agent)"

log "Assert logs query WITHOUT telepresence (client-go port-forward fallback)"
"$TP" status 2>/dev/null | grep -q "Connected" && fail "expected telepresence to be disconnected here"
n_pf=$("$LDBG" logs query orders --since 1h --json | qcount)
[[ "$n_pf" -ge 1 ]] || fail "port-forward fallback query returned nothing (count=$n_pf)"
ok "port-forward fallback works while disconnected (count=$n_pf)"

log "INTEGRATION PASSED"
