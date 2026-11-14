#!/usr/bin/env bash
# down.sh — stop what `make up` or `make remote` started (the data in data/ stays).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN="${ROOT}/.cache/run"
# The pid is `uv run` / `pnpm`; the servers are their descendants: stop the whole tree.
killtree() {
  local child
  for child in $(pgrep -P "$1"); do killtree "$child"; done
  kill "$1" 2>/dev/null
}
for name in ui api tunnel preview; do
  if [[ -f "${RUN}/${name}.pid" ]]; then
    killtree "$(cat "${RUN}/${name}.pid")"
    rm -f "${RUN}/${name}.pid"
  fi
done
rm -f "${RUN}/api.mode" "${RUN}/api.store"
(cd "$ROOT" && COMPOSE_FILE=docker-compose.yml:docker-compose.m2.yml docker compose down >/dev/null 2>&1)
echo "Stopped. Captured data stays in data/ (delete data/telemetry to start over)."
if grep "# aintercept-interception" /etc/hosts 2>/dev/null | grep -q "127\.0\.0\.1"; then
  echo "WARNING: this machine still routes Anthropic traffic to the local proxy, which is now stopped."
  echo "Claude Code can't reach Anthropic until you run: sudo scripts/route-claude-through-proxy.sh revert"
fi
