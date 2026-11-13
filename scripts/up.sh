#!/usr/bin/env bash
#
# up.sh — the whole local stack, one command (`make up`; docs/SETUP.md):
#   Docker:        Envoy (TLS interception, real api.anthropic.com upstream),
#                  certsvc, the observer, Redis, Postgres (its passwords generated into
#                  the gitignored .env) and the rollups (docs/PIPELINE.md)
#   this machine:  the query API (:8787) and the UI (:5173), in the background,
#                  logs and pids in .cache/run/
# Safe to re-run: whatever is already up stays up.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN="${ROOT}/.cache/run"
mkdir -p "$RUN" "${ROOT}/data/telemetry"
bold() { printf '\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\033[31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# --- prerequisites ---------------------------------------------------------------------------
command -v docker >/dev/null || fail "docker is not installed (Docker Desktop, or docker + compose v2)"
docker info >/dev/null 2>&1 || fail "docker is installed but not running: start Docker Desktop and re-run"
docker compose version >/dev/null 2>&1 || fail "docker compose v2 is missing"
command -v uv >/dev/null || fail "uv is not installed: https://docs.astral.sh/uv/ (brew install uv)"
if [[ -s "${NVM_DIR:-$HOME/.nvm}/nvm.sh" ]]; then # use the UI's Node (ui/.nvmrc), installing it if needed
  # shellcheck disable=SC1091
  source "${NVM_DIR:-$HOME/.nvm}/nvm.sh" >/dev/null
  nvm install "$(cat "${ROOT}/ui/.nvmrc")" >/dev/null 2>&1 || true
  nvm use "$(cat "${ROOT}/ui/.nvmrc")" >/dev/null 2>&1 || true
fi
command -v node >/dev/null || fail "node is not installed: Node $(cat "${ROOT}/ui/.nvmrc") (nvm install)"
want="$(cut -d. -f1 < "${ROOT}/ui/.nvmrc" | tr -d v)"
have="$(node -p 'process.versions.node.split(".")[0]')"
[[ "$have" == "$want" ]] || fail "the UI needs Node ${want}; this shell has Node ${have} (nvm install ${want})"
if ! command -v pnpm >/dev/null; then
  corepack enable >/dev/null 2>&1 || fail "pnpm is missing and corepack couldn't enable it (npm i -g corepack)"
fi
others="$(lsof -nP -iTCP:443 -sTCP:LISTEN 2>/dev/null | awk 'NR > 1 && $1 !~ /^com\.docke/ && $1 !~ /^vpnkit/' || true)"
if [[ -n "$others" ]]; then
  echo "$others" | head -3 >&2
  fail "port 443 is in use by something other than Docker (above); the proxy listens there"
fi

# --- the proxy, observer, Redis and rollups (Docker) -----------------------------------------
bold "1/3  Proxy, observer, Redis, Postgres and rollups (Docker; the first build takes a few minutes)"
make -s -C "$ROOT" ca >/dev/null
bash "${ROOT}/scripts/pg-env.sh" # Postgres's passwords, once, into the gitignored .env
(cd "$ROOT" && COMPOSE_FILE=docker-compose.yml:docker-compose.m2.yml docker compose up -d --build --quiet-pull \
  certsvc observer redis postgres rollup envoy >"${RUN}/compose.log" 2>&1) || { tail -20 "${RUN}/compose.log" >&2; fail "docker compose up failed (log: .cache/run/compose.log)"; }

# --- the API and UI (this machine) -----------------------------------------------------------
alive() { [[ -f "${RUN}/$1.pid" ]] && kill -0 "$(cat "${RUN}/$1.pid")" 2>/dev/null; }
port_free() { # port name: fail unless nothing listens there (ours is checked by alive first)
  local pid
  pid="$(lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
  [[ -z "$pid" ]] && return 0
  fail "port $1 (the $2) is in use by pid ${pid} ($(lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p')): stop it, or 'make down' in that checkout"
}
bold "2/3  Query API on :8787"
if ! alive api; then
  port_free 8787 API
  (cd "${ROOT}/api" && uv sync --locked -q)
  # The stateless API over the rollups' commits and merge in data/telemetry (docs/PIPELINE.md §4).
  (cd "${ROOT}/api" || exit 1; AINTERCEPT_STORE=v1 AINTERCEPT_BUCKETS_DIR="${ROOT}/data/telemetry" \
    AINTERCEPT_CACHE_DIR="${ROOT}/.cache/api" AINTERCEPT_REDIS_ADDR=127.0.0.1:16379 \
    nohup uv run python -m aintercept_api.serve --port 8787 >"${RUN}/api.log" 2>&1 </dev/null & echo $! >"${RUN}/api.pid")
fi
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
for _ in $(seq 90); do
  (cd "$ROOT" && docker compose exec -T postgres pg_isready -q -U postgres -d aintercept) 2>/dev/null && break
  sleep 1
done
(cd "$ROOT" && docker compose exec -T postgres pg_isready -q -U postgres -d aintercept) 2>/dev/null \
  || fail "Postgres didn't come up (docker compose logs postgres)"
wait_for "the observer" http://127.0.0.1:9001/healthz compose.log
wait_for "the rollups" http://127.0.0.1:9004/healthz compose.log
wait_for "the API" http://127.0.0.1:8787/api/v1/status api.log
wait_for "the UI" http://localhost:5173/ ui.log

echo
bold "Up."
echo "  UI           http://localhost:5173"
echo "  API          http://localhost:8787/api/v1/status"
echo "  Logs         .cache/run/api.log, .cache/run/ui.log; docker compose logs -f observer rollup"
echo "  Check it     make verify      (one request through the proxy, end to end)"
echo "  Route Claude Code through it (needs sudo; edits /etc/hosts, undo with 'revert'):"
echo "               sudo scripts/route-claude-through-proxy.sh setup 127.0.0.1"
echo "               export NODE_EXTRA_CA_CERTS=\"${ROOT}/certs/root-ca.crt\"; claude"
echo "  Stop         make down"
