# Architecture

This document explains the design decisions the PRD asks us to justify —
especially the telemetry mechanism and the dynamic certificate path — and
distinguishes tested behavior from assumptions.

## Components

```
Test client (curl / Claude Code)
  │ https://api.anthropic.com/...   (DNS override -> proxy; trusts dev root CA)
  ▼
Envoy :443
  │ tls_inspector reads SNI
  │ filter_chain_match selects the cert for the SNI hostname
  │ TLS terminated with a leaf fetched from certsvc over SDS (minted on demand)
  │ HTTP/1.1 or HTTP/2 parsed
  ├── ext_proc ──► observer (Go)  ──► Sink (stdout now, S3 later)
  │   (failure_mode_allow)             buffer-and-ack, drop-on-overflow
  ▼
upstream cluster (verified TLS, real SNI)
  M1: local fixture origin   |   M2+: real api.anthropic.com
```

- **certsvc** (Go): an Envoy SDS v3 gRPC server. Holds the issuing intermediate
  key. On each SDS request, the requested resource name *is* the SNI hostname;
  certsvc checks it against the allowlist and lazily mints (and caches) a
  short-lived leaf. Fails closed for empty/disallowed names.
- **observer** (Go): an Envoy ext_proc v3 gRPC server. Acks every message
  immediately and tees observations onto a bounded in-memory buffer drained by a
  separate goroutine into a `Sink`.
- **fixture** (Go): an HTTPS origin with a recognizable synthetic marker, used
  by M1 to prove MITM without real credentials.
- **Envoy**: TLS termination, per-SNI cert selection, HTTP forwarding, upstream
  TLS verification, and the ext_proc tee.

## Decision 1 — Telemetry mechanism: ext_proc (non-blocking), not tap or ALS

The PRD (§9.1, FR-05) requires us to compare `ext_proc` against tap and
access-log export and justify the pick. The constraints in tension:

- **Must prove decrypted body bytes reached Go** (acceptance criterion:
  "A synthetic fixture proves decrypted body visibility").
- **An observer failure or slowness must never fail or stall a real request**
  (FR-04, FR-05).

| Mechanism | Sees body bytes? | Non-blocking by default? | Fit |
|-----------|------------------|--------------------------|-----|
| `ext_proc` | Yes (STREAMED) | No — but configurable | ✅ chosen |
| tap filter | Yes | More so, but coarse trace format | reshape cost |
| gRPC ALS  | **No (metadata only)** | Yes, unconditionally | fails body criterion |

ALS cannot satisfy the body-visibility criterion by construction, so it is out
as the sole mechanism. tap can carry bodies but emits trace records that we'd
have to reshape into the lifecycle event model; it is the documented fallback.
`ext_proc` is the only single mechanism that carries body chunks *and* can be
made non-blocking. We make it non-blocking three ways:

1. **Envoy side:** `failure_mode_allow: true` means if the observer is down or
   the gRPC stream errors, Envoy continues the request. `message_timeout: 0.2s`
   bounds how long Envoy will wait on the observer per message before, under
   failure-mode-allow, proceeding. **Verified:** smoke test step 6 stops the
   observer and confirms the request still succeeds.
2. **Observer side (the key design):** the ext_proc handler does *no real work*
   on the request path. It copies each observation into an `Event` and calls
   `Processor.Offer`, which is a non-blocking channel send, then immediately
   returns the "continue, no mutation" response to Envoy. A separate goroutine
   drains the buffer into the sink. A slow sink can never back-pressure Envoy;
   it can only fill the buffer.
3. **Overflow policy:** when the bounded buffer is full, `Offer` **drops** the
   event, increments a counter, and raises an alarm (logged loudly on the first
   drop and every N thereafter; surfaced on `/stats`). Losing telemetry is
   always preferred to adding latency to real traffic. **Verified:** unit test
   `TestProcessorDropsWhenBufferFull`.

### Residual risk (documented, per PRD)

With `ext_proc` in STREAMED mode, Envoy does round-trip each message to the
observer. A *dead* observer cannot block (failure_mode_allow), but a
*slow-but-alive* observer could add up to `message_timeout` of latency per
message before Envoy gives up. Because our handler offers-and-returns without
I/O, this is bounded by gRPC/channel mechanics, not by sink speed. If a
benchmark shows this latency is unacceptable, the fallback is **ALS for metadata
+ a narrow tap for body-proof** (two mechanisms). This is not needed for v0.

## Decision 2 — Dynamic per-SNI certificates via SDS + filter_chain_match

The PRD (§3 cert rule, FR-03, §9.2) stresses that "Envoy SDS alone does not
implement arbitrary-SNI, handshake-time certificate minting," and that we must
demonstrate a *working* handshake integration, not a disconnected Go signer.

What we built and verified:

- Envoy's `tls_inspector` listener filter reads SNI before TLS termination.
- A `filter_chain_match.server_names` entry per allowlisted hostname selects the
  right filter chain (hence the right cert) **at handshake time**.
- That chain's `DownstreamTlsContext` references an SDS secret **named for the
  SNI hostname**. On the first connection for that host, Envoy requests the
  secret from certsvc, which mints a leaf for exactly that name and returns it.
  Subsequent connections reuse the cached leaf. **Verified:** smoke test step 1
  shows a leaf with `CN=api.anthropic.com` chaining to the dev root.

### Honest limitation

This is per-SNI selection over a **known allowlist**, not truly *arbitrary* SNI
minting for any hostname seen at the socket. For v0 that is exactly the goal —
issuance is explicitly restricted to the allowlist (`api.anthropic.com`), and
adding a hostname means adding a filter chain + SDS secret config. A fully
arbitrary handshake-time minting path (one filter chain, cert chosen purely by
observed SNI) would require either Envoy's on-demand SDS with a dynamically
derived secret name per SNI, or a minimal dedicated TLS front end. We chose the
stock-Envoy allowlist approach because it is robust, requires no custom TLS
code, and fully satisfies the v0 allowlist requirement. The lazy-minting logic
in `certsvc` already supports any allowlisted name on demand; the only static
piece is the per-host filter chain in `envoy.yaml`.

## Decision 3 — Upstream DNS isolation and real TLS verification

The PRD's critical routing rule: the proxy's upstream resolution must reach the
*real* provider, never the client-overridden address, to avoid loops; and
upstream TLS verification must stay on.

- **M1:** upstream is the `fixture` container, resolved by Docker's internal DNS
  (completely separate from any client-side `/etc/hosts` override). Envoy
  verifies the fixture against the fixture's own CA (`upstream-ca.crt`), which is
  a *different trust domain* from the interception CA — proving verification is
  genuine, not disabled. **Verified:** the stack cannot connect to the fixture
  unless Envoy trusts that CA.
- **M2 (planned):** the upstream cluster becomes a `dynamic_forward_proxy` (or an
  explicit upstream resolver pinned to public DNS) with `sni: api.anthropic.com`
  and the system/public root bundle as `trusted_ca`. The client-side DNS
  override must not be visible to Envoy's resolver. See `limitations.md`.

## Scaling and statelessness (forward-looking)

The observer is designed to run as many replicas with **no sticky routing**:

- Each HTTP transaction corresponds to one ext_proc stream, which stays on one
  observer for its life — so per-transaction correlation needs no shared state.
- Events carry a globally-unique `correlation_id` (instance id + stream counter
  + random suffix) and the `observer_instance`, so many replicas can fan their
  event streams into one durable sink (e.g. an S3 bucket) without coordination.
- The `Sink` interface is the seam: `StdoutSink` today; a batching S3 sink later
  implements the same two methods with zero change to the hot path.

Cross-request aggregation (per-user/session rollups) is explicitly **not** done
in the observer and would belong in a downstream consumer of the event stream.
