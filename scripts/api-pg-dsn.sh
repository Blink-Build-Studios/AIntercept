# api-pg-dsn.sh — sourced by up.sh and `make api`: exports AINTERCEPT_PG_DSN, the API login's DSN for
# the local Postgres (Compose publishes it on 127.0.0.1:15432), with its password from the gitignored
# .env (scripts/pg-env.sh writes it). It goes into the environment only: never a command line, never
# printed. No .env, or no API password in it: it fails, because the API won't start without a DSN.
# shellcheck shell=bash
_api_pg_env="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.env"
_api_pg_pw="$(sed -n 's/^AINTERCEPT_PG_API_PASSWORD=//p' "$_api_pg_env" 2>/dev/null | head -1)"
if [[ -z "$_api_pg_pw" ]]; then
  echo "ERROR: no AINTERCEPT_PG_API_PASSWORD in ${_api_pg_env}: run 'make up' first (it starts Postgres and writes .env)" >&2
  unset _api_pg_env _api_pg_pw
  return 1 2>/dev/null || exit 1
fi
export AINTERCEPT_PG_DSN="postgres://aintercept_api:${_api_pg_pw}@127.0.0.1:15432/aintercept?sslmode=disable&keepalives_idle=10&keepalives_interval=5&keepalives_count=3&tcp_user_timeout=30000"
unset _api_pg_env _api_pg_pw
