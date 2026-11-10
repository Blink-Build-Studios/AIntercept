#!/usr/bin/env bash
#
# dev-aws-emulators.sh — prepare the local AWS emulators started by
# docker-compose.aws.yml, mirroring what a production AWS setup provides:
#
#   1. an asymmetric KMS signing key (alias/aintercept-dev-intermediate) in
#      local-kms, standing in for the intermediate's KMS key;
#   2. an intermediate CA certificate issued by the dev root for that key's
#      PUBLIC key and name-constrained to the routed domains, written to
#      certs/kms/ (there is no intermediate private key file at all);
#   3. the observer's key-hash salt as a Secrets Manager secret in moto.
#
# Step 2 is the ceremony a partner's PKI performs against their real KMS key.
# Requires: the dev root (make ca), the aws CLI, openssl >= 3.
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERTS="${ROOT}/certs"
OUT="${CERTS}/kms"
KMS_ENDPOINT="${KMS_ENDPOINT:-http://localhost:5056}"
SM_ENDPOINT="${SM_ENDPOINT:-http://localhost:5055}"
ALIAS="alias/aintercept-dev-intermediate"
PERMITTED_DOMAINS="${PERMITTED_DOMAINS:-anthropic.com claude.ai openai.com chatgpt.com oaiusercontent.com generativelanguage.googleapis.com cloudflare-ech.com}"

export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1
unset AWS_PROFILE

for f in root-ca.crt root-ca.key; do
  [[ -f "${CERTS}/${f}" ]] || { echo "missing ${CERTS}/${f}; run 'make ca' first" >&2; exit 1; }
done
command -v aws >/dev/null || { echo "the aws CLI is required" >&2; exit 1; }

# Wait for both emulators.
for ep in "${KMS_ENDPOINT}" "${SM_ENDPOINT}"; do
  for _ in $(seq 30); do curl -s -o /dev/null "${ep}" && break; sleep 1; done
done

echo "==> KMS signing key (${ALIAS})"
KEY_ID="$(aws --endpoint-url "${KMS_ENDPOINT}" kms create-key \
  --key-spec ECC_NIST_P256 --key-usage SIGN_VERIFY \
  --description "aintercept dev intermediate" --query KeyMetadata.KeyId --output text)"
aws --endpoint-url "${KMS_ENDPOINT}" kms delete-alias --alias-name "${ALIAS}" 2>/dev/null || true
aws --endpoint-url "${KMS_ENDPOINT}" kms create-alias --alias-name "${ALIAS}" --target-key-id "${KEY_ID}"

echo "==> Intermediate certificate for the KMS public key"
mkdir -p "${OUT}"
aws --endpoint-url "${KMS_ENDPOINT}" kms get-public-key --key-id "${ALIAS}" \
  --query PublicKey --output text | base64 -d > "${OUT}/kms-pub.der"
openssl pkey -pubin -inform DER -in "${OUT}/kms-pub.der" -out "${OUT}/kms-pub.pem"
cat > "${OUT}/intermediate.ext" <<EXT
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
extendedKeyUsage=serverAuth
nameConstraints=critical$(printf ',permitted;DNS:%s' ${PERMITTED_DOMAINS})
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid
EXT
openssl x509 -new -force_pubkey "${OUT}/kms-pub.pem" \
  -subj "/O=AI Interception Proxy (DEV ONLY)/CN=AI Interception Dev KMS Issuing CA" \
  -CA "${CERTS}/root-ca.crt" -CAkey "${CERTS}/root-ca.key" \
  -days 30 -sha256 -extfile "${OUT}/intermediate.ext" \
  -out "${OUT}/intermediate-ca.crt"
cat "${OUT}/intermediate-ca.crt" "${CERTS}/root-ca.crt" > "${OUT}/ca-chain.crt"
rm -f "${OUT}/kms-pub.der" "${OUT}/intermediate.ext"
openssl verify -CAfile "${CERTS}/root-ca.crt" "${OUT}/intermediate-ca.crt"

echo "==> Secrets Manager salt (aintercept-dev-key-hash-salt)"
aws --endpoint-url "${SM_ENDPOINT}" secretsmanager delete-secret \
  --secret-id aintercept-dev-key-hash-salt --force-delete-without-recovery >/dev/null 2>&1 || true
aws --endpoint-url "${SM_ENDPOINT}" secretsmanager create-secret \
  --name aintercept-dev-key-hash-salt \
  --secret-string "{\"salt\":\"$(openssl rand -hex 32)\"}" >/dev/null
echo "done"
