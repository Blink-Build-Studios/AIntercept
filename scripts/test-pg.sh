#!/usr/bin/env bash
#
# test-pg.sh — a throwaway Postgres for the tests (docs/PIPELINE.md, Testing): the pinned image
# docker-compose.yml runs, in a container named aintercept-test-pg on 127.0.0.1:15433 (trust auth,
# localhost only, no volume). Started if it isn't running; prints the admin DSN the tests read
# from AINTERCEPT_TEST_PG. `make check` uses it when AINTERCEPT_TEST_PG isn't set (CI sets it to its
# service container). Stop it with `docker rm -f aintercept-test-pg`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME=aintercept-test-pg
PORT="${AINTERCEPT_TEST_PG_PORT:-15433}"
DSN="postgres://postgres@127.0.0.1:${PORT}/postgres?sslmode=disable"
# The one pin: docker-compose.yml's postgres image (scripts/check-pins.py holds CI to it too).
IMAGE="$(sed -n 's/^ *image: *\(postgres:[^ ]*\).*/\1/p' "${ROOT}/docker-compose.yml" | head -1)"
[[ -n "$IMAGE" ]] || { echo "test-pg: no postgres image in docker-compose.yml" >&2; exit 1; }

if [[ "$(docker inspect -f '{{.State.Running}} {{.Config.Image}}' "$NAME" 2>/dev/null)" != "true ${IMAGE}" ]]; then
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:5432" \
    -e POSTGRES_HOST_AUTH_METHOD=trust \
    -e POSTGRES_INITDB_ARGS="--locale-provider=builtin --builtin-locale=C.UTF-8 --encoding=UTF8" \
    --tmpfs /var/lib/postgresql/data \
    "$IMAGE" -c max_locks_per_transaction=256 -c max_connections=200 -c fsync=off >/dev/null
fi
for _ in $(seq 60); do
  docker exec "$NAME" pg_isready -q -h 127.0.0.1 -U postgres 2>/dev/null && { echo "$DSN"; exit 0; }
  sleep 0.5
done
docker logs "$NAME" 2>&1 | tail -20 >&2
echo "test-pg: Postgres didn't become ready" >&2
exit 1
