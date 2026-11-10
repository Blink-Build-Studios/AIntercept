#!/usr/bin/env bash
#
# verify.sh — prove the local stack end to end (`make verify`; docs/SETUP.md):
# one request through the proxy to the real api.anthropic.com (no key, so the
# upstream answers 401), then wait until the observer, the processor and the
# API all show it. Changes nothing on this machine: curl's --resolve pins the
# host to the proxy for this one request.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ok() { printf '  \033[32mok\033[0m    %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$*" >&2; exit 1; }
count() { curl -fsS http://127.0.0.1:9002/stats | python3 -c 'import json,sys; print(json.load(sys.stdin)["requests"])'; }

before="$(count 2>/dev/null)" || fail "the processor isn't answering on :9002 (make up)"
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 --cacert "${ROOT}/certs/root-ca.crt" \
  --resolve api.anthropic.com:443:127.0.0.1 -X POST -H 'content-type: application/json' -d '{}' \
  https://api.anthropic.com/v1/messages)" || fail "TLS through the proxy failed (is Docker up? make up)"
[[ "$code" == "401" || "$code" == "400" ]] || fail "expected the real upstream's 401, got HTTP ${code}"
ok "the proxy intercepted TLS for api.anthropic.com and reached the real upstream (HTTP ${code})"

for _ in $(seq 60); do
  now="$(count)"
  [[ "$now" -gt "$before" ]] && break
  sleep 1
done
[[ "$now" -gt "$before" ]] || fail "the processor didn't see it within 60 s (docker compose logs observer process)"
ok "the observer recorded it and the processor shaped it (${now} requests in the tables)"

for _ in $(seq 30); do
  first="$(curl -fsS http://127.0.0.1:8787/api/v1/status | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["firstRequestAt"] or "")')"
  [[ -n "$first" ]] && break
  sleep 1
done
[[ -n "$first" ]] || fail "the API doesn't show it (.cache/run/api.log)"
ok "the API serves it (first request at ${first})"
echo "Verified. Next: route Claude Code through the proxy (docs/SETUP.md step 4) and open http://localhost:5173"
