# Limitations and gaps (v0)

Honest accounting of what this proof of concept does and does not do, per the
PRD's instruction to distinguish tested behavior from assumptions.

## What is tested (M1, verified by `make test`)

- Dynamic per-SNI leaf issuance for the allowlisted host, chaining to the dev
  root CA, selected at TLS handshake time.
- Cached-cert reuse across connections; fail-closed for non-allowlisted SNI.
- Envoy decrypts HTTP/2, forwards to a genuine upstream over verified TLS, and
  returns the response.
- Correlated request/response lifecycle events in Go with stable IDs and byte
  counts; redaction of secrets by default; decrypted body visibility provable
  with `DEV_LOG_BODIES=true` and a synthetic marker.
- Streaming/SSE through the proxy without full-body buffering.
- Observer outage does not break a proxied request; drops are observable.

## What is NOT yet done / assumed

- **Real `api.anthropic.com` (M2) and Claude Code (M3)** are not yet wired. M1
  uses a local fixture. Reaching the real provider requires:
  - Swapping the `upstream` cluster for a `dynamic_forward_proxy` or an explicit
    public-DNS resolver with `sni: api.anthropic.com` and the public root bundle
    as `trusted_ca` — crucially resolving the *real* address, isolated from any
    client-side DNS override.
  - A test machine `/etc/hosts` (or dedicated resolver) entry and dev-root trust
    installed, per `claude-code-test.md`.
- **Arbitrary-SNI handshake minting.** Issuance is restricted to an allowlist
  with one Envoy filter chain per host. Truly arbitrary minting (cert chosen
  purely from observed SNI, no per-host config) is not implemented. See
  `architecture.md` Decision 2.
- **HTTP/3 / QUIC interception is out of scope.** DNS redirection + TCP TLS MITM
  does not capture QUIC. Clients must fall back to TCP/HTTP2 for interception.
  Envoy's HTTP/3 support does **not** by itself establish transparent QUIC MITM.
- **Client trust stores.** Claude Code may use Node.js-specific trust
  configuration rather than (or in addition to) the OS store. M3 must validate
  this explicitly. If a client pins certificates or restricts trust, that is a
  reported limitation — we do not disable protections to force interception.
- **Traffic capture scope.** DNS redirection only captures clients using the
  overridden resolver and connecting to the resulting IP. Hardcoded IPs,
  DoH/DoT, QUIC, pinned clients, and non-allowlisted hostnames are not captured.
- **ext_proc latency.** See `architecture.md` Decision 1 residual risk. No
  latency/throughput benchmark has been run yet (PRD §5 asks for one; it is a
  follow-up, not a milestone blocker).
- **Kubernetes (M4)** manifests are provided as a path but the cluster
  deployment has not been exercised end-to-end in this pass.

## Security notes

- The interception **root private key is never mounted into any container.** It
  signs the intermediate once, then should move offline.
- The issuing **intermediate key** is mounted read-only into `certsvc` only.
- Secrets and bodies are redacted by default. `DEV_LOG_BODIES=true` prints
  bounded body previews for local dev only and must never be set in Kubernetes.
  Even then, sensitive headers (Authorization, API keys, cookies) are always
  redacted.
- Upstream TLS verification is never disabled.
