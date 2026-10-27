# Manual Claude Code test (M3) — procedure

> Status: **not yet executed.** This is the documented procedure for M3. It
> depends on M2 (real `api.anthropic.com` upstream) being wired first.

The goal is to run Claude Code normally against its usual Anthropic endpoint
(no proxy-specific base URL), with the test machine's DNS and CA trust
configured, and confirm (a) the operation succeeds, (b) streaming behaves
normally, and (c) Go receives corresponding decrypted HTTP events.

Use an **authorized test account** and a **test-only prompt**. Do not route
production credentials or sensitive content through the dev interception stack.

## 1. Start the stack and read connection info

```bash
make run
make info   # prints the host-reachable IP, port 443, CA path, DNS override
```

## 2. Trust the dev root CA (reversible)

Install **only** `certs/root-ca.crt` (the public root cert). Never install any
private key.

- **macOS (system keychain):**
  ```bash
  sudo security add-trusted-cert -d -r trustRoot \
    -k /Library/Keychains/System.keychain certs/root-ca.crt
  ```
- **Linux (Debian/Ubuntu):**
  ```bash
  sudo cp certs/root-ca.crt /usr/local/share/ca-certificates/ai-interception-dev-root.crt
  sudo update-ca-certificates
  ```
- **Node.js / Claude Code:** Node maintains its own trust store. Point it at the
  dev root for the test session:
  ```bash
  export NODE_EXTRA_CA_CERTS="$PWD/certs/root-ca.crt"
  ```
  This is the most likely place interception "silently fails" if skipped — see
  `limitations.md` on client trust behavior.

## 3. Redirect DNS for the intercepted hostname (test machine only)

```bash
# Replace 127.0.0.1 with the host-reachable IP from `make info` if the client
# is a different machine.
echo "127.0.0.1  api.anthropic.com" | sudo tee -a /etc/hosts
# Verify (dig does NOT read /etc/hosts):
getent hosts api.anthropic.com || dscacheutil -q host -a name api.anthropic.com
```

## 4. Run Claude Code with a test-only prompt

Run Claude Code as usual (no custom API base URL). Issue a short, benign prompt.
In another terminal, watch the observer:

```bash
make observer-logs
```

## 5. Confirm

- Claude Code completes the operation and streaming output appears normally.
- The observer prints correlated `request_headers` / `response_body_chunk` /
  `complete` events for `api.anthropic.com` with a stable `correlation_id` and
  byte counts.
- The `Authorization` / API-key headers appear **redacted**, never in the clear.

If certificate pinning or a client trust restriction prevents interception,
**report that as a limitation** (in `limitations.md`) rather than disabling any
protection or changing the test objective.

## 6. Clean up (return the machine to its prior state)

```bash
# Remove the hosts entry:
sudo sed -i.bak '/api\.anthropic\.com/d' /etc/hosts     # Linux
# (macOS: edit /etc/hosts and remove the line)

# Untrust the dev root:
#   macOS:  sudo security delete-certificate -c "AI Interception Dev Root CA" /Library/Keychains/System.keychain
#   Linux:  sudo rm /usr/local/share/ca-certificates/ai-interception-dev-root.crt && sudo update-ca-certificates --fresh
unset NODE_EXTRA_CA_CERTS

make stop
```
