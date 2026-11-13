#!/usr/bin/env bash
#
# pg-env.sh — the local Postgres's passwords (docker-compose.yml, docs/PIPELINE.md §6): generated
# once into the gitignored .env, which Compose reads; never committed, never printed. Keys already
# there are kept, so re-running changes nothing. The passwords are set when the pgdata volume is
# first created: delete that volume (docker volume rm) along with them, or the two disagree.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV="${ROOT}/.env"
touch "$ENV"
chmod 600 "$ENV"
secret() { openssl rand -hex 24 2>/dev/null || python3 -c 'import secrets; print(secrets.token_hex(24))'; }
for key in AINTERCEPT_PG_PASSWORD AINTERCEPT_PG_WORKER_PASSWORD AINTERCEPT_PG_API_PASSWORD; do
  grep -q "^${key}=" "$ENV" || printf '%s=%s\n' "$key" "$(secret)" >>"$ENV"
done
