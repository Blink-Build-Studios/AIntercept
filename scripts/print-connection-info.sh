#!/usr/bin/env bash
#
# print-connection-info.sh — print the exact DNS + trust configuration a test
# client needs, using a HOST-REACHABLE address (never a container-only IP).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CERTS_DIR="${CERTS_DIR:-${ROOT}/certs}"
ROOT_CA="${CERTS_DIR}/root-ca.crt"
HOSTNAME_INTERCEPT="${CERT_ALLOWLIST:-api.anthropic.com}"
PORT=443

# Determine a host-reachable IP. On Docker Desktop (macOS/Windows), published
# ports are reachable at 127.0.0.1. On Linux with host networking, use the
# primary LAN IP. We prefer an explicit override, then loopback (Docker
# Desktop), and surface the LAN IP as an alternative.
host_ip() {
  if [[ -n "${PROXY_HOST_IP:-}" ]]; then
    echo "${PROXY_HOST_IP}"
    return
  fi
  echo "127.0.0.1"
}

lan_ip() {
  case "$(uname -s)" in
    Darwin)
      ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true ;;
    Linux)
      ip -4 route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}' || true ;;
  esac
}

IP="$(host_ip)"
LAN="$(lan_ip || true)"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }

bold "================ AI Interception Proxy — connection info ================"
echo "Proxy listening:    ${IP}:${PORT}"
echo "Intercepted host:   ${HOSTNAME_INTERCEPT}"
if [[ -f "${ROOT_CA}" ]]; then
  echo "CA certificate:     ${ROOT_CA}"
else
  echo "CA certificate:     (missing — run 'make ca')"
fi
echo "DNS override:       ${HOSTNAME_INTERCEPT} -> ${IP}"
echo "Go observer logs:   docker compose logs -f observer   (JSON events on stdout)"
echo "Envoy admin:        http://127.0.0.1:9901"
echo "Observer stats:     http://127.0.0.1:9001/stats"
echo
bold "Test client setup (local, reversible):"
echo "  1) Trust the dev root CA (see docs/claude-code-test.md for per-OS steps)."
echo "  2) Add to /etc/hosts:   ${IP}   ${HOSTNAME_INTERCEPT}"
echo "     (Note: /etc/hosts is NOT reflected by 'dig'; verify with 'getent hosts ${HOSTNAME_INTERCEPT}'"
echo "      or 'curl --resolve ${HOSTNAME_INTERCEPT}:443:${IP} ...')."
if [[ -n "${LAN}" && "${IP}" == "127.0.0.1" ]]; then
  echo
  echo "  If the test client is a DIFFERENT machine, 127.0.0.1 will NOT reach this host."
  echo "  Use this host's LAN IP instead:   PROXY_HOST_IP=${LAN} make info"
fi
bold "========================================================================"
