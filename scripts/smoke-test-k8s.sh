#!/usr/bin/env bash
#
# smoke-test-k8s.sh — post-install end-to-end proof for a cluster deployment.
# Drives a request at an allowlisted host through the Envoy LoadBalancer and
# asserts a telemetry object lands in the object store (S3 / DO Spaces).
#
# Env:
#   SPACE          object store bucket / Space name (required)
#   ENDPOINT       S3-compatible endpoint (required for DO Spaces/MinIO)
#   PREFIX         key prefix (default: aintercept)
#   AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY  creds to LIST the bucket (required)
#   HOST           allowlisted interception host (default api.anthropic.com)
#   ROOT_CA        dev root CA path (default certs/root-ca.crt)
#   MAX_WAIT       seconds to wait for an object (default 120)
set -uo pipefail

SPACE="${SPACE:?set SPACE}"
ENDPOINT="${ENDPOINT:?set ENDPOINT}"
PREFIX="${PREFIX:-aintercept}"
HOST="${HOST:-api.anthropic.com}"
ROOT_CA="${ROOT_CA:-certs/root-ca.crt}"
MAX_WAIT="${MAX_WAIT:-120}"

pass=0; fail=0
ok()  { echo "  PASS: $*"; pass=$((pass+1)); }
bad() { echo "  FAIL: $*"; fail=$((fail+1)); }

echo "==> 1) Resolve the Envoy LoadBalancer external address"
IP=$(kubectl get svc envoy -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null)
if [[ -z "$IP" ]]; then
  IP=$(kubectl get svc envoy -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null)
fi
if [[ -n "$IP" ]]; then ok "envoy LB at $IP"; else bad "no LoadBalancer ingress yet"; echo "aborting"; exit 1; fi

echo "==> 2) Drive an intercepted request through the proxy"
CODE=$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$ROOT_CA" \
  --resolve "${HOST}:443:${IP}" \
  -X POST "https://${HOST}/v1/messages" --data '{}' 2>/dev/null)
# Any HTTP response (even 400/401/405 from the real upstream) proves the full
# intercept→upstream→client path worked. A curl failure (000) means it didn't.
if [[ "$CODE" != "000" && -n "$CODE" ]]; then ok "proxied request returned HTTP $CODE (intercept path works)"; else bad "no HTTP response through the proxy"; fi

echo "==> 3) Wait for a telemetry object to land in the store (<= ${MAX_WAIT}s)"
deadline=$(( $(date +%s) + MAX_WAIT ))
found=""
while [[ $(date +%s) -lt $deadline ]]; do
  n=$(aws s3 ls "s3://${SPACE}/${PREFIX}/" --recursive --endpoint-url "$ENDPOINT" 2>/dev/null | grep -c '\.ndjson\.gz')
  if [[ "${n:-0}" -ge 1 ]]; then found=1; break; fi
  sleep 5
done
if [[ -n "$found" ]]; then
  ok "telemetry object(s) present in s3://${SPACE}/${PREFIX}/"
  echo "    latest objects:"
  aws s3 ls "s3://${SPACE}/${PREFIX}/" --recursive --endpoint-url "$ENDPOINT" 2>/dev/null | tail -3 | sed 's/^/      /'
else
  bad "no telemetry object appeared within ${MAX_WAIT}s"
fi

echo
echo "================ k8s smoke: ${pass} passed, ${fail} failed ================"
[[ ${fail} -eq 0 ]]
