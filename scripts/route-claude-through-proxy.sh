#!/usr/bin/env bash
#
# route-claude-through-proxy.sh — point this machine's Anthropic traffic at the
# aintercept interception proxy (e.g. the DOKS LoadBalancer), so Claude Code and
# any other Anthropic client route through it, then cleanly revert.
#
# WHAT IT CHANGES (all reversible):
#   /etc/hosts : for each routed host, adds
#                  `<LB_IP>  <host>`              (IPv4)
#                  `::ffff:<LB_IP>  <host>`       (IPv6, IPv4-mapped)
#                GLOBAL — every process on this machine that calls the host now
#                hits the proxy. The IPv6 line is required: an A-only override
#                leaves the provider's real AAAA record in place, and on an
#                IPv6-capable network Node connects straight to the provider,
#                bypassing the proxy (docs/INTERCEPTION_ISSUES.md C6). The
#                IPv4-mapped address reaches the proxy over IPv4, so the load
#                balancer needs no IPv6 support.
#   It does NOT modify the system keychain; client trust is handled per-process
#   via NODE_EXTRA_CA_CERTS (printed below), so nothing system-wide is trusted.
#
# Usage:
#   scripts/route-claude-through-proxy.sh setup [LB_IP]   # add hosts entries + verify
#   scripts/route-claude-through-proxy.sh revert          # remove hosts entries
#   scripts/route-claude-through-proxy.sh status          # show current state
#
# ROUTE_HOSTS overrides the routed hosts (space-separated), e.g.
#   ROUTE_HOSTS="api.anthropic.com chatgpt.com" scripts/route-claude-through-proxy.sh setup
#
# After `setup`, launch Claude Code in a shell that trusts the dev root:
#   export NODE_EXTRA_CA_CERTS="$PWD/certs/root-ca.crt"
#   claude
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ROOT_CA="${ROOT}/certs/root-ca.crt"
read -r -a HOSTS <<< "${ROUTE_HOSTS:-api.anthropic.com}"
HOST="${HOSTS[0]}"
MARKER="# aintercept-interception"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*"; }
ok()   { printf '\033[32m%s\033[0m\n' "$*"; }

hosts_entry_ip() {
  grep -E "^\s*[0-9.]+\s+${HOST}\s+${MARKER}\s*$" /etc/hosts 2>/dev/null | awk '{print $1}' | head -1
}

# Every address the system resolver returns for $1 (IPv4 and IPv6). dscacheutil
# goes through the same resolver as applications, /etc/hosts included, and
# reports AAAA records even on a network without an IPv6 route.
resolved_addrs() {
  if command -v dscacheutil >/dev/null 2>&1; then
    dscacheutil -q host -a name "$1" 2>/dev/null | awk '/^ip(v6)?_address:/{print $2}'
  else
    getent ahosts "$1" 2>/dev/null | awk '{print $1}'
  fi | sort -u
}

# Fails when any routed host resolves to an address other than the proxy's.
verify_resolution() {
  local ip="$1" h a bad=0
  for h in "${HOSTS[@]}"; do
    for a in $(resolved_addrs "$h"); do
      if [[ "$a" != "$ip" && "$a" != "::ffff:${ip}" ]]; then
        warn "${h} also resolves to ${a}: traffic to it would bypass the proxy."
        bad=1
      fi
    done
  done
  return "$bad"
}

cmd_setup() {
  local ip="${1:-}"
  if [[ -z "$ip" ]]; then
    # Try to read the LB IP from kubectl if not supplied.
    ip="$(kubectl get svc envoy -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)"
  fi
  [[ -n "$ip" ]] || { warn "No LB IP given and couldn't read it from kubectl. Pass it: setup <LB_IP>"; exit 2; }
  [[ -f "$ROOT_CA" ]] || { warn "Dev root CA not found at $ROOT_CA — run scripts/generate-dev-ca.sh"; exit 2; }

  local existing; existing="$(hosts_entry_ip || true)"
  if [[ -n "$existing" ]]; then
    warn "Already routing ${HOST} → ${existing}. Reverting first for a clean set."
    cmd_revert
  fi

  local h
  for h in "${HOSTS[@]}"; do
    bold "Adding /etc/hosts overrides: ${ip} and ::ffff:${ip}  ${h}  (needs sudo)"
    printf '%s  %s  %s\n::ffff:%s  %s  %s\n' "$ip" "$h" "$MARKER" "$ip" "$h" "$MARKER" \
      | sudo tee -a /etc/hosts >/dev/null
  done
  sudo dscacheutil -flushcache 2>/dev/null || true
  sudo killall -HUP mDNSResponder 2>/dev/null || true

  bold "Verifying that IPv4 and IPv6 both resolve to the proxy..."
  if ! verify_resolution "$ip"; then
    warn "Leaving the hosts entries in place; run 'revert' to undo."
    exit 1
  fi
  ok "Every routed host resolves only to the proxy."

  bold "Verifying TLS + reachability through the proxy..."
  local code
  code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$ROOT_CA" \
    -X POST "https://${HOST}/v1/messages" --data '{}' --max-time 10 2>/dev/null || echo 000)"
  if [[ "$code" == "000" ]]; then
    warn "Proxy did not respond (HTTP 000). Check the LB IP and that the stack is up."
    warn "Leaving the hosts entry in place; run 'revert' to undo."
    exit 1
  fi
  ok "Proxy responded (HTTP ${code}) — interception path is live."
  echo
  bold "Now launch Claude Code so Node trusts the dev root CA:"
  echo "    export NODE_EXTRA_CA_CERTS=\"${ROOT_CA}\""
  echo "    claude"
  echo
  warn "This is GLOBAL: all ${HOSTS[*]} traffic on this machine now routes through the proxy."
  warn "Revert when done:  scripts/route-claude-through-proxy.sh revert"
}

cmd_revert() {
  if [[ -z "$(hosts_entry_ip || true)" ]]; then
    ok "No aintercept hosts entry present — nothing to revert."
    return 0
  fi
  bold "Removing the aintercept overrides from /etc/hosts (needs sudo)"
  # Delete only our marked lines, leaving the rest of /etc/hosts untouched.
  # Portable in-place delete of our marked line (sed -i differs on macOS vs
  # Linux, so filter to a temp file and move it back).
  local tmp
  tmp="$(mktemp)"
  grep -v "${MARKER}\$" /etc/hosts | sudo tee "$tmp" >/dev/null
  sudo cp "$tmp" /etc/hosts
  rm -f "$tmp"
  sudo dscacheutil -flushcache 2>/dev/null || true
  sudo killall -HUP mDNSResponder 2>/dev/null || true
  ok "Reverted. ${HOSTS[*]} resolve normally again."
}

cmd_status() {
  local ip; ip="$(hosts_entry_ip || true)"
  if [[ -n "$ip" ]]; then
    bold "ROUTING THROUGH PROXY: ${HOSTS[*]} → ${ip}"
    local h
    for h in "${HOSTS[@]}"; do echo "${h} resolves to: $(resolved_addrs "$h" | tr '\n' ' ')"; done
    if verify_resolution "$ip"; then ok "No bypass: IPv4 and IPv6 both go to the proxy."; else exit 1; fi
  else
    ok "Normal: ${HOST} is NOT being redirected."
  fi
}

case "${1:-}" in
  setup)  cmd_setup "${2:-}";;
  revert) cmd_revert;;
  status) cmd_status;;
  *) echo "Usage: $0 {setup [LB_IP]|revert|status}"; exit 1;;
esac
