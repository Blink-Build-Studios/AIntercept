#!/usr/bin/env bash
#
# generate-dev-ca.sh — create a DEVELOPMENT-ONLY two-tier CA for the
# interception proxy:
#
#   root CA (trust anchor)  --signs-->  issuing intermediate CA
#
# Only the ROOT PUBLIC certificate is installed in a test client's trust
# store. The intermediate PRIVATE key is handed to the Go cert service to mint
# short-lived leaf certificates on demand. The ROOT PRIVATE key is used only to
# sign the intermediate, then should be moved offline (see the final notice).
#
# This CA is scoped to local testing. Do NOT install it on machines handling
# real traffic you do not own.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CERTS_DIR="${CERTS_DIR:-$(cd "${SCRIPT_DIR}/.." && pwd)/certs}"

ROOT_DAYS="${ROOT_DAYS:-3650}"
INT_DAYS="${INT_DAYS:-1825}"
KEY_BITS="${KEY_BITS:-4096}"

ROOT_KEY="${CERTS_DIR}/root-ca.key"
ROOT_CRT="${CERTS_DIR}/root-ca.crt"
INT_KEY="${CERTS_DIR}/intermediate-ca.key"
INT_CSR="${CERTS_DIR}/intermediate-ca.csr"
INT_CRT="${CERTS_DIR}/intermediate-ca.crt"
CHAIN_CRT="${CERTS_DIR}/ca-chain.crt"

red()  { printf '\033[31m%s\033[0m\n' "$*"; }
bold() { printf '\033[1m%s\033[0m\n' "$*"; }

mkdir -p "${CERTS_DIR}"
chmod 700 "${CERTS_DIR}"

if [[ -f "${ROOT_KEY}" || -f "${INT_KEY}" ]]; then
  red "Refusing to overwrite existing CA material in ${CERTS_DIR}."
  red "Run 'make clean' (which warns before deleting CA keys) first if you really want to regenerate."
  exit 1
fi

bold "==> Generating development ROOT CA (trust anchor)"
openssl genrsa -out "${ROOT_KEY}" "${KEY_BITS}"
chmod 600 "${ROOT_KEY}"
openssl req -x509 -new -nodes -key "${ROOT_KEY}" -sha256 -days "${ROOT_DAYS}" \
  -out "${ROOT_CRT}" \
  -subj "/O=AI Interception Proxy (DEV ONLY)/CN=AI Interception Dev Root CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:1" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"

bold "==> Generating issuing INTERMEDIATE CA"
openssl genrsa -out "${INT_KEY}" "${KEY_BITS}"
chmod 600 "${INT_KEY}"
openssl req -new -key "${INT_KEY}" \
  -out "${INT_CSR}" \
  -subj "/O=AI Interception Proxy (DEV ONLY)/CN=AI Interception Dev Issuing CA"

# Intermediate must be a CA that can issue end-entity (server-auth) leaves but
# not further CAs: pathlen:0.
cat > "${CERTS_DIR}/intermediate.ext" <<EXT
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
extendedKeyUsage=serverAuth
EXT

openssl x509 -req -in "${INT_CSR}" \
  -CA "${ROOT_CRT}" -CAkey "${ROOT_KEY}" -CAcreateserial \
  -out "${INT_CRT}" -days "${INT_DAYS}" -sha256 \
  -extfile "${CERTS_DIR}/intermediate.ext"

cat "${INT_CRT}" "${ROOT_CRT}" > "${CHAIN_CRT}"
rm -f "${INT_CSR}" "${CERTS_DIR}/intermediate.ext"

bold "==> Verifying intermediate chains to root"
openssl verify -CAfile "${ROOT_CRT}" "${INT_CRT}"

bold "==> CA material written to ${CERTS_DIR}:"
printf '  %s\n' \
  "root-ca.crt           (INSTALL THIS in the test client trust store)" \
  "root-ca.key           (ROOT PRIVATE KEY — move OFFLINE after this step)" \
  "intermediate-ca.crt   (issuing CA public cert)" \
  "intermediate-ca.key   (issuing PRIVATE KEY — mounted into the Go cert service only)" \
  "ca-chain.crt          (intermediate + root, for leaf chain assembly)"

echo
red "SECURITY NOTICE"
red "  * root-ca.key is only needed to sign/rotate the intermediate. For anything"
red "    beyond local dev, move it to offline storage and delete it from this host."
red "  * intermediate-ca.key must be supplied to the cert service via a protected"
red "    secret (Docker secret / Kubernetes Secret) — never baked into an image,"
red "    committed to git, or printed to logs."
red "  * This root is for test interception only. Uninstall it from the client"
red "    trust store when finished (see docs/claude-code-test.md)."
