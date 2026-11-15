#!/usr/bin/env bash
#
# verify.sh — prove the local stack end to end (`make verify`; docs/SETUP.md):
# one request through the proxy to the real api.anthropic.com (no key, so the
# upstream answers 401), then wait until the observer has written it, a worker
# has committed it to Postgres (the tail found it in the recent index), the
# workers' lag has settled and the API serves that request from Postgres (its
# drawer, /api/v1/requests/<id>). Then v9 (docs/PIPELINE.md §2): one analytics
# batch with a made-up event, posted to a path the upstream doesn't serve (404:
# nothing reaches its analytics), and both requests found in the raw files the
# observer streams, in Postgres's calls and events as the v9 workers load them,
# and the call's raw range read back alone. Changes nothing on this machine:
# curl's --resolve pins the host to the proxy for these requests.
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
RAW="${ROOT}/data/telemetry/raw"
raws() { { find "$RAW" -type f -name '*.ndjson.gz' 2>/dev/null || true; } | wc -l | tr -d ' '; }
raw_before="$(raws)"
verify_session="verify-$(python3 -c 'import uuid; print(uuid.uuid4())')"
batch="$(python3 -c 'import json,sys,uuid,datetime; print(json.dumps({"events":[{"event_type":"ClaudeCodeInternalEvent","event_data":{
  "event_name":"aintercept_verify","event_id":str(uuid.uuid4()),"session_id":sys.argv[1],
  "client_timestamp":datetime.datetime.now(datetime.timezone.utc).isoformat()}}]}))' "$verify_session")"
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 --cacert "${ROOT}/certs/root-ca.crt" \
  --resolve api.anthropic.com:443:127.0.0.1 -X POST -H 'content-type: application/json' -d "$batch" \
  https://api.anthropic.com/api/event_logging/aintercept-verify)" || fail "TLS through the proxy failed for the analytics batch"
ok "an analytics batch went through the proxy (HTTP ${code} from the upstream)"
# The observer holds a batch until its next row (the batch's last event may still be coming): one
# more request releases it.
curl -sS -o /dev/null --max-time 20 --cacert "${ROOT}/certs/root-ca.crt" --resolve api.anthropic.com:443:127.0.0.1 \
  -X POST -H 'content-type: application/json' -d '{}' https://api.anthropic.com/v1/messages || fail "TLS through the proxy failed"

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
  || ', event_rows ' || (SELECT count(*) FROM event_rows) || ', session_rows ' || (SELECT count(*) FROM session_rows)
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
# --- v9 (docs/PIPELINE.md §2): raw files in the store, loaded into the fact tables ---------------
curl -fsS -o /dev/null http://127.0.0.1:9005/healthz || fail "the v9 workers aren't answering on :9005 (docker compose logs worker)"
for _ in $(seq 90); do
  [[ "$(raws)" -gt "$raw_before" ]] && break
  sleep 1
done
[[ "$(raws)" -gt "$raw_before" ]] || fail "the observer wrote no raw file within 90 s (docker compose logs observer)"
ok "the observer streamed a raw file (data/telemetry/raw/)"
v9=""
for _ in $(seq 90); do
  v9="$(psql_q "SELECT c.call_id || ' ' || c.raw_key || ' ' || c.raw_offset || ' ' || c.raw_len
    FROM calls c WHERE c.host = 'api.anthropic.com' AND c.path = '/v1/messages' AND c.start_ns >= ${sent_ns}
    AND c.end_ns IS NOT NULL AND c.raw_key IS NOT NULL ORDER BY c.start_ns DESC LIMIT 1" 2>/dev/null || true)"
  evs="$(psql_q "SELECT count(*) FROM events WHERE session_id = '${verify_session}'" 2>/dev/null || echo 0)"
  [[ -n "$v9" && "${evs:-0}" -gt 0 ]] && break
  sleep 1
done
[[ -n "$v9" ]] || fail "no calls row with a raw range for the request within 90 s (docker compose logs worker)"
[[ "${evs:-0}" -gt 0 ]] || fail "no events row for the analytics batch within 90 s (docker compose logs worker)"
read -r v9_id v9_key v9_off v9_len <<<"$v9"
ok "a v9 worker loaded them: calls ${v9_id}, events ${evs} (session ${verify_session})"
python3 -I - "${ROOT}/data/telemetry/${v9_key}" "$v9_off" "$v9_len" "$v9_id" <<'PY' || fail "the call's raw range doesn't read back as its own gzip member"
import json, sys, zlib
path, off, n, call = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
with open(path, "rb") as f:
    f.seek(off)
    member = f.read(n)
d = zlib.decompressobj(wbits=31)
lines = d.decompress(member).decode().splitlines()
if not d.eof or d.unused_data:
    sys.exit("not exactly one gzip member")
rows = [json.loads(l) for l in lines if not l.startswith('{"record":')]
if not rows or any(r.get("correlation_id") != call for r in rows):
    sys.exit("the member's rows aren't the call's")
PY
ok "its raw range (${v9_key} @${v9_off}+${v9_len}) reads back alone as the call's rows"
lag9="$(curl -fsS http://127.0.0.1:9005/lag)" || fail "the v9 workers' /lag errors"
ok "the v9 workers' /lag: ${lag9}"
echo "Verified. Next: route Claude Code through the proxy (docs/SETUP.md step 4) and open http://localhost:5173"
