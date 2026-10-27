#!/usr/bin/env bash
#
# generate-fixture-certs.sh — create a SEPARATE CA and server leaf for the M1
# fixture HTTPS origin. This trust domain is intentionally distinct from the
# interception CA: it lets us prove that Envoy performs REAL upstream TLS
# verification (it must trust this CA to connect to the fixture), rather than
# having upstream verification disabled.
#
# Outputs into certs/fixture/:
#   upstream-ca.crt   -> mounted into Envoy as the upstream trusted_ca
#   fixture.crt/.key  -> served by the fixture, SAN = fixture / fixture.local
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CERTS_DIR="${CERTS_DIR:-$(cd "${SCRIPT_DIR}/.." && pwd)/certs}"
OUT="${CERTS_DIR}/fixture"
mkdir -p "${OUT}"
chmod 700 "${OUT}"

CA_KEY="${OUT}/upstream-ca.key"
CA_CRT="${OUT}/upstream-ca.crt"
LEAF_KEY="${OUT}/fixture.key"
LEAF_CSR="${OUT}/fixture.csr"
LEAF_CRT="${OUT}/fixture.crt"

if [[ -f "${CA_KEY}" ]]; then
  echo "fixture CA already present in ${OUT}; skipping."
  exit 0
fi

echo "==> fixture upstream CA"
openssl genrsa -out "${CA_KEY}" 4096
openssl req -x509 -new -nodes -key "${CA_KEY}" -sha256 -days 3650 \
  -out "${CA_CRT}" \
  -subj "/O=AI Interception Proxy Fixture (DEV)/CN=Fixture Upstream CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"

echo "==> fixture server leaf (SAN: fixture, fixture.local, localhost)"
openssl genrsa -out "${LEAF_KEY}" 2048
openssl req -new -key "${LEAF_KEY}" -out "${LEAF_CSR}" \
  -subj "/O=AI Interception Proxy Fixture (DEV)/CN=fixture.local"

cat > "${OUT}/fixture.ext" <<EXT
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:fixture,DNS:fixture.local,DNS:localhost
EXT

openssl x509 -req -in "${LEAF_CSR}" \
  -CA "${CA_CRT}" -CAkey "${CA_KEY}" -CAcreateserial \
  -out "${LEAF_CRT}" -days 825 -sha256 \
  -extfile "${OUT}/fixture.ext"

rm -f "${LEAF_CSR}" "${OUT}/fixture.ext"
chmod 600 "${CA_KEY}" "${LEAF_KEY}"

echo "==> verifying fixture leaf chains to fixture CA"
openssl verify -CAfile "${CA_CRT}" "${LEAF_CRT}"
echo "fixture certs written to ${OUT}"
