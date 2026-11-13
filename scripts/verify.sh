#!/usr/bin/env bash
#
# verify.sh — prove the local stack end to end (`make verify`; docs/SETUP.md):
# one request through the proxy to the real api.anthropic.com (no key, so the
# upstream answers 401), then wait until the observer has written it, a rollup
# has committed it and the API shows it. Changes nothing on this machine: curl's --resolve pins the
# host to the proxy for this one request.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ok() { printf '  \033[32mok\033[0m    %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$*" >&2; exit 1; }
V1="${ROOT}/data/telemetry/v1/keys"
# Files under v1/keys/: ingest files (in/) and the rollups' commits (commit/).
count() { { find "$V1" -path "*/$1/*" -type f 2>/dev/null || true; } | wc -l | tr -d ' '; }

curl -fsS -o /dev/null http://127.0.0.1:9004/healthz || fail "the rollups aren't answering on :9004 (make up)"
ins="$(count in)"
commits="$(count commit)"
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 --cacert "${ROOT}/certs/root-ca.crt" \
  --resolve api.anthropic.com:443:127.0.0.1 -X POST -H 'content-type: application/json' -d '{}' \
  https://api.anthropic.com/v1/messages)" || fail "TLS through the proxy failed (is Docker up? make up)"
[[ "$code" == "401" || "$code" == "400" ]] || fail "expected the real upstream's 401, got HTTP ${code}"
ok "the proxy intercepted TLS for api.anthropic.com and reached the real upstream (HTTP ${code})"

wait_more() { # what before: until there are more files of that kind, 60 s at most
  for _ in $(seq 60); do
    [[ "$(count "$1")" -gt "$2" ]] && return 0
    sleep 1
  done
  return 1
}
wait_more in "$ins" || fail "the observer wrote no ingest file within 60 s (docker compose logs observer)"
ok "the observer recorded it (an ingest file under data/telemetry/v1/keys/)"
wait_more commit "$commits" || fail "no rollup committed it within 60 s (docker compose logs rollup)"
ok "a rollup applied it (a new commit; $(count commit) in all)"

for _ in $(seq 30); do
  first="$(curl -fsS http://127.0.0.1:8787/api/v1/status | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["firstRequestAt"] or "")')"
  [[ -n "$first" ]] && break
  sleep 1
done
[[ -n "$first" ]] || fail "the API doesn't show it (.cache/run/api.log)"
ok "the API serves it (first request at ${first})"
echo "Verified. Next: route Claude Code through the proxy (docs/SETUP.md step 4) and open http://localhost:5173"
