#!/usr/bin/env bash
#
# verify.sh — prove the local stack end to end (`make verify`; docs/SETUP.md):
# one request through the proxy to the real api.anthropic.com (no key, so the
# upstream answers 401), then wait until the observer has written it, a worker
# has committed it to Postgres (the tail found it in the recent index), the
# workers' lag has settled and the API serves that request from Postgres (its
# drawer, /api/v1/requests/<id>). Changes nothing on this machine:
# curl's --resolve pins the host to the proxy for this one request.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ok() { printf '  \033[32mok\033[0m    %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$*" >&2; exit 1; }
V1="${ROOT}/data/telemetry/v1/keys"
# Ingest files under v1/keys/<key>/in/.
count() { { find "$V1" -path "*/in/*" -type f 2>/dev/null || true; } | wc -l | tr -d ' '; }
# A query as Postgres's admin, inside its container (the local socket: no password).
psql_q() { (cd "$ROOT" && docker compose exec -T postgres psql -qtA -U postgres -d aintercept -c "$1"); }
# Every commit so far: each key's commit number, summed.
commits() { psql_q "SELECT coalesce(sum(n), 0) FROM keys" 2>/dev/null || echo 0; }
lag() { curl -fsS http://127.0.0.1:9004/lag 2>/dev/null; }

curl -fsS -o /dev/null http://127.0.0.1:9004/healthz || fail "the workers aren't answering on :9004 (make up)"
lag >/dev/null || fail "the workers can't read Postgres (/lag errors: docker compose logs rollup postgres)"
ins="$(count)"
before="$(commits)"
sent_ns="$(python3 -c 'import time; print(time.time_ns() - 5_000_000_000)')" # a little slack for clocks
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 --cacert "${ROOT}/certs/root-ca.crt" \
  --resolve api.anthropic.com:443:127.0.0.1 -X POST -H 'content-type: application/json' -d '{}' \
  https://api.anthropic.com/v1/messages)" || fail "TLS through the proxy failed (is Docker up? make up)"
[[ "$code" == "401" || "$code" == "400" ]] || fail "expected the real upstream's 401, got HTTP ${code}"
ok "the proxy intercepted TLS for api.anthropic.com and reached the real upstream (HTTP ${code})"

for _ in $(seq 60); do
  [[ "$(count)" -gt "$ins" ]] && break
  sleep 1
done
[[ "$(count)" -gt "$ins" ]] || fail "the observer wrote no ingest file within 60 s (docker compose logs observer)"
ok "the observer recorded it (an ingest file under data/telemetry/v1/keys/)"

for _ in $(seq 60); do
  [[ "$(commits)" -gt "$before" ]] && break
  sleep 1
done
after="$(commits)"
[[ "$after" -gt "$before" ]] || fail "no worker committed it within 60 s (docker compose logs rollup)"
ok "a worker committed it to Postgres (commits: ${before} -> ${after})"
psql_q "SELECT 'keys ' || (SELECT count(*) FROM keys) || ', requests ' || (SELECT count(*) FROM requests)
  || ', request_txs ' || (SELECT count(*) FROM request_txs) || ', agent_calls ' || (SELECT count(*) FROM agent_calls)
  || ', analytics ' || (SELECT count(*) FROM analytics) || ', session_rows ' || (SELECT count(*) FROM session_rows)
  || ', flagged ' || (SELECT count(*) FROM dirty_keys)" | sed 's/^/        rows: /'

settled=""
for _ in $(seq 60); do
  body="$(lag || true)"
  if [[ -n "$body" ]] && python3 -c 'import json,sys; d=json.loads(sys.argv[1]); sys.exit(0 if d["oldest_unapplied_seconds"] < 30 and d["quarantined_keys"] == 0 else 1)' "$body"; then
    settled="$body"
    break
  fi
  sleep 1
done
[[ -n "$settled" ]] || fail "the workers' lag didn't settle under 30 s (last /lag: ${body:-none})"
ok "the workers' lag settled (/lag: ${settled})"

store="$(cat "${ROOT}/.cache/run/api.store" 2>/dev/null || echo pg)"
if [[ "$store" == "pg" ]]; then
  # This request's id, as the worker committed it (the newest api.anthropic.com call since we sent it).
  id="$(psql_q "SELECT call_id FROM requests WHERE host = 'api.anthropic.com' AND start_ns >= ${sent_ns}
    AND call_id IS NOT NULL ORDER BY start_ns DESC LIMIT 1" 2>/dev/null || true)"
  [[ -n "$id" ]] || fail "no requests row for it in Postgres (docker compose logs rollup)"
  got=""
  for _ in $(seq 30); do
    got="$(curl -fsS "http://127.0.0.1:8787/api/v1/requests/${id}" 2>/dev/null \
      | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(d["requestId"], d["method"], d["path"])' 2>/dev/null || true)"
    [[ "$got" == "${id} POST /v1/messages" ]] && break
    sleep 1
  done
  [[ "$got" == "${id} POST /v1/messages" ]] || fail "the API doesn't serve request ${id} (got: ${got:-nothing}; .cache/run/api.log)"
  ok "the API serves it from Postgres (/api/v1/requests/${id}: POST /v1/messages)"
  first="$(curl -fsS http://127.0.0.1:8787/api/v1/status | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["lastIngestAt"] or "")')"
  [[ -n "$first" ]] || fail "the API's /status has no lastIngestAt (.cache/run/api.log)"
  ok "the API's /status reads Postgres (last ingest at ${first})"
else
  curl -fsS -o /dev/null http://127.0.0.1:8787/api/v1/status || fail "the API isn't answering (.cache/run/api.log)"
  ok "the API answers (store v1, the v5 folders, which the workers no longer write; the default store is pg)"
fi
echo "Verified. Next: route Claude Code through the proxy (docs/SETUP.md step 4) and open http://localhost:5173"
