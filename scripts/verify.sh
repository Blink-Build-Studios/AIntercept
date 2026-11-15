#!/usr/bin/env bash
#
# verify.sh — prove the local stack end to end (`make verify`; docs/SETUP.md): one request through
# the proxy to the real api.anthropic.com (no key, so the upstream answers 401) and one analytics
# batch with a made-up event, posted to a path the upstream doesn't serve (404: nothing reaches its
# analytics). Then (docs/PIPELINE.md §2): both are in the raw files the observer streams, a worker
# has loaded them into Postgres's calls and events, the workers' lag has settled, the API shows them
# on their pages (the call's drawer, the batch's session's Events tab), the call's raw range reads
# back alone, and the observer holds no Postgres connection. Changes nothing on this machine: curl's
# --resolve pins the host to the proxy for these requests.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ok() { printf '  \033[32mok\033[0m    %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$*" >&2; exit 1; }
RAW="${ROOT}/data/telemetry/raw"
raws() { { find "$RAW" -type f -name '*.ndjson.gz' 2>/dev/null || true; } | wc -l | tr -d ' '; }
# A query as Postgres's admin, inside its container (the local socket: no password).
psql_q() { (cd "$ROOT" && docker compose exec -T postgres psql -qtA -U postgres -d aintercept -c "$1"); }
lag() { curl -fsS http://127.0.0.1:9005/lag 2>/dev/null; }

curl -fsS -o /dev/null http://127.0.0.1:9005/healthz || fail "the workers aren't answering on :9005 (make up)"
lag >/dev/null || fail "the workers can't read Postgres (/lag errors: docker compose logs worker postgres)"
raw_before="$(raws)"
sent_ns="$(python3 -c 'import time; print(time.time_ns() - 5_000_000_000)')" # a little slack for clocks
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 --cacert "${ROOT}/certs/root-ca.crt" \
  --resolve api.anthropic.com:443:127.0.0.1 -X POST -H 'content-type: application/json' -d '{}' \
  https://api.anthropic.com/v1/messages)" || fail "TLS through the proxy failed (is Docker up? make up)"
[[ "$code" == "401" || "$code" == "400" ]] || fail "expected the real upstream's 401, got HTTP ${code}"
ok "the proxy intercepted TLS for api.anthropic.com and reached the real upstream (HTTP ${code})"
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

for _ in $(seq 90); do
  [[ "$(raws)" -gt "$raw_before" ]] && break
  sleep 1
done
[[ "$(raws)" -gt "$raw_before" ]] || fail "the observer wrote no raw file within 90 s (docker compose logs observer)"
ok "the observer streamed a raw file (data/telemetry/raw/)"

row=""
for _ in $(seq 90); do
  row="$(psql_q "SELECT c.call_id || ' ' || c.raw_key || ' ' || c.raw_offset || ' ' || c.raw_len
    FROM calls c WHERE c.host = 'api.anthropic.com' AND c.path = '/v1/messages' AND c.start_ns >= ${sent_ns}
    AND c.end_ns IS NOT NULL AND c.raw_key IS NOT NULL ORDER BY c.start_ns DESC LIMIT 1" 2>/dev/null || true)"
  evs="$(psql_q "SELECT count(*) FROM events WHERE session_id = '${verify_session}'" 2>/dev/null || echo 0)"
  [[ -n "$row" && "${evs:-0}" -gt 0 ]] && break
  sleep 1
done
[[ -n "$row" ]] || fail "no calls row with a raw range for the request within 90 s (docker compose logs worker)"
[[ "${evs:-0}" -gt 0 ]] || fail "no events row for the analytics batch within 90 s (docker compose logs worker)"
read -r id key off len <<<"$row"
ok "a worker loaded them into Postgres: calls ${id}, events ${evs} (session ${verify_session})"

settled=""
for _ in $(seq 60); do
  body="$(lag || true)"
  if [[ -n "$body" ]] && python3 -c 'import json,sys; sys.exit(0 if json.loads(sys.argv[1])["oldest_unloaded_seconds"] < 30 else 1)' "$body"; then
    settled="$body"
    break
  fi
  sleep 1
done
[[ -n "$settled" ]] || fail "the workers' lag didn't settle under 30 s (last /lag: ${body:-none})"
ok "the workers' lag settled (/lag: ${settled})"

got=""
for _ in $(seq 30); do
  got="$(curl -fsS "http://127.0.0.1:8787/api/v1/requests/${id}" 2>/dev/null \
    | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(d["requestId"], d["method"], d["path"])' 2>/dev/null || true)"
  [[ "$got" == "${id} POST /v1/messages" ]] && break
  sleep 1
done
[[ "$got" == "${id} POST /v1/messages" ]] || fail "the API doesn't show request ${id} (got: ${got:-nothing}; .cache/run/api.log)"
ok "the API shows the call on its page (/api/v1/requests/${id}: POST /v1/messages)"
names=""
for _ in $(seq 30); do
  names="$(curl -fsS "http://127.0.0.1:8787/api/v1/sessions/${verify_session}/events?internal=true" 2>/dev/null \
    | python3 -c 'import json,sys; print(",".join(e["name"] for e in json.load(sys.stdin)["data"]))' 2>/dev/null || true)"
  [[ "$names" == *aintercept_verify* ]] && break
  sleep 1
done
[[ "$names" == *aintercept_verify* ]] || fail "the API doesn't show the batch's event (got: ${names:-nothing}; .cache/run/api.log)"
ok "the API shows the event on its session's Events tab (/api/v1/sessions/${verify_session}/events)"
first="$(curl -fsS http://127.0.0.1:8787/api/v1/status | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["lastIngestAt"] or "")')"
[[ -n "$first" ]] || fail "the API's /status has no lastIngestAt (.cache/run/api.log)"
ok "the API's /status reads Postgres (last ingest at ${first})"

python3 -I - "${ROOT}/data/telemetry/${key}" "$off" "$len" "$id" <<'PY' || fail "the call's raw range doesn't read back as its own gzip member"
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
ok "its raw range (${key} @${off}+${len}) reads back alone as the call's rows"

observer="$(cd "$ROOT" && docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$(docker compose ps -q observer)")"
[[ -n "${observer// /}" ]] || fail "can't find the observer container's address (docker compose ps observer)"
conns=0
for ip in $observer; do
  n="$(psql_q "SELECT count(*) FROM pg_stat_activity WHERE client_addr = '${ip}'")" || fail "can't read Postgres's connections"
  conns=$((conns + n))
done
[[ "$conns" -eq 0 ]] || fail "the observer holds ${conns} Postgres connections (it should have none)"
ok "the observer holds no Postgres connection (ingest writes only raw files)"
echo "Verified. Next: route Claude Code through the proxy (docs/SETUP.md step 4) and open http://localhost:5173"
