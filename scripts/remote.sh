#!/usr/bin/env bash
#
# remote.sh — the UI on this machine against the query API running in the
# cluster (`make remote`), as a client network runs it: the API sits inside
# the cluster next to the data (deploy/helm/aintercept/templates/api.yaml), and
# the UI reaches it through a tunnel. The tunnel is `kubectl port-forward` to
# the API's ClusterIP Service on localhost:8787 (it reconnects itself when the
# API pod restarts); the UI dev server proxies /api there as usual.
# Needs kubectl pointed at the cluster and the chart's api.enabled=true.
# Logs and pids in .cache/run/; `make down` stops it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN="${ROOT}/.cache/run"
NS="${REMOTE_NAMESPACE:-default}"
mkdir -p "$RUN"
bold() { printf '\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\033[31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# --- prerequisites ---------------------------------------------------------------------------
command -v kubectl >/dev/null || fail "kubectl is not installed"
if [[ -s "${NVM_DIR:-$HOME/.nvm}/nvm.sh" ]]; then
  # shellcheck disable=SC1091
  source "${NVM_DIR:-$HOME/.nvm}/nvm.sh" >/dev/null
  nvm use "$(cat "${ROOT}/ui/.nvmrc")" >/dev/null 2>&1 || true
fi
want="$(cut -d. -f1 < "${ROOT}/ui/.nvmrc" | tr -d v)"
have="$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo none)"
[[ "$have" == "$want" ]] || fail "the UI needs Node ${want}; this shell has Node ${have} (nvm install ${want})"
alive() { [[ -f "${RUN}/$1.pid" ]] && kill -0 "$(cat "${RUN}/$1.pid")" 2>/dev/null; }
alive api && fail "the API from 'make up' is running on this machine: 'make down' first"
port_free() {
  local pid
  pid="$(lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
  [[ -z "$pid" ]] || fail "port $1 (the $2) is in use by pid ${pid}: stop it, or 'make down' in that checkout"
}

# --- the tunnel ------------------------------------------------------------------------------
bold "1/3  The cluster's API (context $(kubectl config current-context), namespace ${NS})"
kubectl -n "$NS" get svc api >/dev/null 2>&1 \
  || fail "no 'api' Service in namespace ${NS}: deploy the chart with api.enabled=true (docs/SETUP.md)"
kubectl -n "$NS" rollout status deploy/api --timeout=120s >/dev/null || fail "the API isn't ready: kubectl -n ${NS} describe deploy/api"
bold "2/3  Tunnel: localhost:8787 → svc/api"
if ! alive tunnel; then
  port_free 8787 tunnel
  # port-forward binds one pod and outlives it (a rollout leaves it erroring, not exiting),
  # so a watchdog restarts it once the API stops answering through it.
  nohup bash -c "
    trap 'kill \$pf 2>/dev/null; exit' TERM INT
    while true; do
      kubectl -n '${NS}' port-forward svc/api 8787:8787 & pf=\$!
      sleep 5; fails=0
      while kill -0 \$pf 2>/dev/null; do
        if curl -fs -m 10 -o /dev/null http://127.0.0.1:8787/api/v1/status; then fails=0; else fails=\$((fails + 1)); fi
        [ \$fails -ge 2 ] && { echo 'tunnel: the API stopped answering; reconnecting'; kill \$pf; }
        sleep 5
      done
      wait \$pf 2>/dev/null; sleep 2
    done" >"${RUN}/tunnel.log" 2>&1 </dev/null &
  echo $! >"${RUN}/tunnel.pid"
fi

# --- the UI ----------------------------------------------------------------------------------
bold "3/3  UI on :5173"
if ! alive ui; then
  port_free 5173 UI
  (cd "${ROOT}/ui" && pnpm install --frozen-lockfile --silent)
  (cd "${ROOT}/ui" || exit 1; nohup pnpm dev --port 5173 --strictPort >"${RUN}/ui.log" 2>&1 </dev/null & echo $! >"${RUN}/ui.pid")
fi
wait_for() { # name url log
  for _ in $(seq 90); do
    curl -fsS -o /dev/null "$2" 2>/dev/null && return 0
    sleep 1
  done
  tail -20 "${RUN}/$3" >&2
  fail "$1 didn't come up at $2 (log: .cache/run/$3)"
}
wait_for "the tunnel to the API" http://127.0.0.1:8787/api/v1/status tunnel.log
wait_for "the UI" http://localhost:5173/ ui.log
people="$(curl -fsS http://127.0.0.1:8787/api/v1/people | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))')"

echo
bold "Up, on the cluster's API (${people} people)."
echo "  UI           http://localhost:5173"
echo "  API          http://localhost:8787 (tunnel to svc/api; log: .cache/run/tunnel.log)"
echo "  API logs     kubectl -n ${NS} logs deploy/api -c api   (the mirror sidecar: -c mirror)"
echo "  Stop         make down"
