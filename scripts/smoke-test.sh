#!/usr/bin/env bash
#
# smoke-test.sh — end-to-end M1 proof. Assumes `make run` has started the stack.
#
# It drives a client at the intercepted hostname (api.anthropic.com) through the
# proxy using --resolve (so no real /etc/hosts edit is needed for CI) and the
# dev root CA as the trust anchor, then asserts:
#
#   1. The TLS handshake succeeds with a leaf chaining to the dev root, issued
#      for the allowlisted SNI (interception actually happened).
#   2. A non-allowlisted SNI fails closed (no cert minted).
#   3. The decrypted SYNTHETIC marker produced by the fixture appears in the
#      observer's event output (decrypted bytes reached Go).
#   4. Streaming (SSE) works through the proxy.
#   5. Stopping the observer does NOT break a proxied request (fail-open tee).
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CERTS_DIR="${CERTS_DIR:-${ROOT}/certs}"
ROOT_CA="${CERTS_DIR}/root-ca.crt"
HOST="${CERT_ALLOWLIST:-api.anthropic.com}"
PROXY_IP="${PROXY_HOST_IP:-127.0.0.1}"
MARKER="SYNTHETIC-FIXTURE-MARKER-7f3a9c"
COMPOSE="docker compose"

pass=0; fail=0
ok()   { echo "  PASS: $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL: $*"; fail=$((fail+1)); }
info() { echo "==> $*"; }

trap 'cleanup' EXIT
cleanup() {
  # Make sure we leave the observer running regardless of where we failed.
  (cd "${ROOT}" && ${COMPOSE} start observer >/dev/null 2>&1) || true
}

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing dependency: $1"; exit 2; }
}
require curl
require openssl

info "1) Intercepted handshake presents a dev-root-chained leaf for ${HOST}"
LEAF="$(echo | openssl s_client -connect "${PROXY_IP}:443" -servername "${HOST}" 2>/dev/null \
  | openssl x509 -noout -subject -issuer 2>/dev/null)"
if echo "${LEAF}" | grep -q "CN=${HOST}"; then
  ok "leaf subject CN=${HOST}: ${LEAF//$'\n'/ }"
else
  bad "expected leaf CN=${HOST}; got: ${LEAF:-<none>}"
fi

VERIFY="$(echo | openssl s_client -connect "${PROXY_IP}:443" -servername "${HOST}" \
  -CAfile "${ROOT_CA}" 2>/dev/null | grep -E 'Verify return code')"
if echo "${VERIFY}" | grep -q '0 (ok)'; then
  ok "leaf verifies against dev root CA (${VERIFY})"
else
  bad "leaf did not verify against dev root CA: ${VERIFY:-<none>}"
fi

info "2) Decrypted request/response reaches the observer, and roundtrip works"
BODY="hello-from-smoke-$(date +%s 2>/dev/null || echo t)"
RESP="$(curl -sS --cacert "${ROOT_CA}" --resolve "${HOST}:443:${PROXY_IP}" \
  -X POST "https://${HOST}/echo" --data "${BODY}" 2>/dev/null)"
if echo "${RESP}" | grep -q "${MARKER}"; then
  ok "proxied POST /echo returned fixture marker"
else
  bad "proxied POST /echo did not return expected content: ${RESP:-<none>}"
fi

# Give the async observer a moment to flush, then look for decrypted evidence.
sleep 1
LOGS="$(cd "${ROOT}" && ${COMPOSE} logs --since 30s observer 2>/dev/null)"
if echo "${LOGS}" | grep -q '"host":"'"${HOST}"'"'; then
  ok "observer emitted correlated events for ${HOST}"
else
  bad "observer did not emit events referencing ${HOST}"
fi
if echo "${LOGS}" | grep -q 'response_body_chunk'; then
  ok "observer saw response_body_chunk events (streaming body observation)"
else
  bad "observer did not record response body chunks"
fi

info "3) Decrypted BODY visibility proof (requires DEV_LOG_BODIES=true stack)"
if echo "${LOGS}" | grep -q "${MARKER}"; then
  ok "observer printed the decrypted synthetic marker (body bytes reached Go)"
else
  echo "  SKIP: marker not in observer logs. Re-run the stack with DEV_LOG_BODIES=true"
  echo "        (DEV_LOG_BODIES=true make run) to prove decrypted body visibility."
fi

info "4) Streaming/SSE works through the proxy"
STREAM="$(curl -sS --cacert "${ROOT_CA}" --resolve "${HOST}:443:${PROXY_IP}" \
  "https://${HOST}/stream" 2>/dev/null)"
if echo "${STREAM}" | grep -q 'event: done'; then
  ok "SSE stream completed through the proxy"
else
  bad "SSE stream did not complete: ${STREAM:-<none>}"
fi

info "5) Non-allowlisted SNI fails closed"
BADHOST="evil.example.com"
if echo | openssl s_client -connect "${PROXY_IP}:443" -servername "${BADHOST}" 2>/dev/null \
    | openssl x509 -noout -subject >/dev/null 2>&1; then
  bad "proxy issued a cert for non-allowlisted host ${BADHOST}"
else
  ok "proxy refused TLS for non-allowlisted host ${BADHOST}"
fi

info "6) Observer outage does not break a proxied request (fail-open tee)"
(cd "${ROOT}" && ${COMPOSE} stop observer >/dev/null 2>&1)
sleep 1
RESP2="$(curl -sS --cacert "${ROOT_CA}" --resolve "${HOST}:443:${PROXY_IP}" \
  "https://${HOST}/" 2>/dev/null)"
if echo "${RESP2}" | grep -q "${MARKER}"; then
  ok "request succeeded with observer stopped (telemetry failed open)"
else
  bad "request failed while observer was down: ${RESP2:-<none>}"
fi
(cd "${ROOT}" && ${COMPOSE} start observer >/dev/null 2>&1)

echo
echo "================ smoke test: ${pass} passed, ${fail} failed ================"
[[ ${fail} -eq 0 ]]
