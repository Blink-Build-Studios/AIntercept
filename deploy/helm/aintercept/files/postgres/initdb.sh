#!/bin/sh
# initdb.sh — Postgres's first start, in Compose and in the cluster (docs/PIPELINE.md §6): the
# image's entrypoint has created the aintercept database (built-in C.UTF-8 locale,
# POSTGRES_INITDB_ARGS); this creates the two logins in it (logins.sql, a copy of
# aintercept/internal/pg/logins.sql that the chart test holds equal) with the passwords from the
# environment: scripts/up.sh's .env locally, the aintercept-postgres Secret in the cluster.
set -eu
: "${AINTERCEPT_PG_WORKER_PASSWORD:?the worker login's password (.env, or the Secret)}"
: "${AINTERCEPT_PG_API_PASSWORD:?the API login's password (.env, or the Secret)}"
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v worker_password="$AINTERCEPT_PG_WORKER_PASSWORD" -v api_password="$AINTERCEPT_PG_API_PASSWORD" \
  -f /aintercept/logins.sql
