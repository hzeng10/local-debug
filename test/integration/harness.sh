#!/usr/bin/env bash
# End-to-end integration harness: kind + Istio ambient + the sample app, driving the
# real ldbg up -> test -> down flow and asserting the hard-won behaviors don't regress
# (ambient opt-out apply/revert, in-cluster takeover, outbound via telepresence, clean
# teardown). Designed for CI (GitHub Actions ubuntu-latest, which has passwordless sudo
# for the telepresence root daemon). Run locally only on a throwaway machine.
#
# Usage:  test/integration/harness.sh
# Env:    TELEPRESENCE_VERSION (default 2.29.0), KEEP=1 to skip teardown for debugging.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
TELEPRESENCE_VERSION="${TELEPRESENCE_VERSION:-2.29.0}"
TEL_IMAGE="ghcr.io/telepresenceio/tel2:${TELEPRESENCE_VERSION}"
WHOAMI_IMAGE="traefik/whoami:v1.10.2"
CURL_IMAGE="curlimages/curl:8.10.1"
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
for t in docker kind kubectl istioctl go python3 curl; do require "$t"; done
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

log "INTEGRATION PASSED"
