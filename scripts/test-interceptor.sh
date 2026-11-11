#!/usr/bin/env bash
# End-to-end test of the Interceptor (docs/INTERCEPTOR.md) through a real
# Envoy: builds and starts test/interceptor's stack (Envoy with the observing
# and enforcing filters, the observer, the example redact Interceptor, the
# fixture), runs aintercept/internal/e2e against it, and tears it down.
#   KEEP=1 leaves the stack running.
#   INTERCEPTOR_COMPOSE_OVERRIDE=<compose file> runs your own Interceptor as the
#   `redact` service instead (path relative to the repo root; the tests expect
#   the example's rules: redact "silly clown", refuse "forbidden-model").
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT/test/interceptor"
rm -rf "$DIR/out" && mkdir -p "$DIR/out"
FILES=(-f "$DIR/compose.yml")
if [ -n "${INTERCEPTOR_COMPOSE_OVERRIDE:-}" ]; then
  FILES+=(-f "$(cd "$ROOT" && cd "$(dirname "$INTERCEPTOR_COMPOSE_OVERRIDE")" && pwd)/$(basename "$INTERCEPTOR_COMPOSE_OVERRIDE")")
fi
dc() { docker compose --project-directory "$DIR" "${FILES[@]}" "$@"; }
cleanup() { [ "${KEEP:-}" = 1 ] || dc down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
dc up -d --build --wait --wait-timeout 180 2>/dev/null || dc up -d --build
for _ in $(seq 1 60); do
  curl -sf -o /dev/null -H 'Host: observe.test' http://localhost:10000/ && break
  sleep 1
done
cd "$ROOT/aintercept"
E2E_COMPOSE_FILES="${FILES[*]}" E2E_COMPOSE_DIR="$DIR" E2E_EVENTS="$DIR/out/events.ndjson" go test -tags e2e -count=1 -v ./internal/e2e/
