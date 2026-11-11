# Spec: uniform interception for HTTP and WebSocket (observe and enforce)

Status: **Phase 1 implemented** (2026-11-02, observer v17; results in §5).
**Phases 2–3 implemented as the Interceptor** (2026-11-10), with a different
interface from §3.5 and §3.7: one `Interceptor` service with `OnCallStart`,
`OnMessage` and `OnCallComplete`, the same in observe and enforce, and
Interceptor content as its own setting. `docs/INTERCEPTOR.md` is the
reference; issue #6 has the decisions. §3.5 and §3.7 below are superseded.
Decisions taken in review are in §0.

## 0. Decisions (2026-11-02)

| Question | Decision |
|---|---|
| Default mode | **observe** |
| How observe vs enforce is configured | Two layers: **data-plane scope** (which hosts/paths are inline, Helm) and **policy scope** (which connections the policy acts on, decided live by `OnConnection`); see §3.7 |
| Policy service unreachable / over deadline in enforce | **Fail closed** (`policy.onError: block`) |
| WebSocket `Drop` / block | **Emulate the provider's own rejection** (the frames and close behavior OpenAI itself uses), captured from real traffic before implementation (Phase 3, first task) |
| Interface shape | **`OnConnection` + `OnMessage`** gRPC service, as in §3.5 |
Evidence comes from the spike described in §2 (Envoy v1.39.6, our pinned version).

## 1. Problem and principles

Codex opens a WebSocket for every model turn. Through the proxy today that
upgrade **hangs ~15 s**, then Codex falls back to HTTP: a Codex task took
2m11s through DO vs 51s through a fixed proxy. That is the proxy degrading a
client, which is unacceptable for this product.

Principles:

1. **Never degrade or downgrade a transport.** Whatever protocol a client
   negotiates (HTTP/1.1, HTTP/2, SSE, WebSocket) passes through at full
   function. We don't refuse WebSockets to force a fallback.
2. **One interception interface for every transport.** A policy is written
   once against *messages*. Whether a message arrived as an HTTP body, an SSE
   event or a WebSocket frame is an implementation detail.
3. **Observe is the default and has zero impact on traffic.** No added
   latency, and an observer outage never affects a request.
4. **Enforce is opt-in, explicit about its cost.** Acting on traffic means
   sitting inline. Latency and failure behavior are stated, tested and
   configurable, not discovered.

Out of scope: HTTP/3 / QUIC interception; writing a customer's policy engine
(we ship the interface and a reference policy); WebSocket parsing for providers
other than OpenAI/Codex (the decoder is generic, the extractors come later).

## 2. Spike findings (evidence)

Isolated harness: Envoy → a test ext_proc server that decodes WebSocket frames
→ a WebSocket echo server, plus real Codex 0.164.0 through the local stack.

| Test | Result |
|---|---|
| Current mode (`STREAMED`) + WebSocket upgrade | **Hangs.** ext_proc gets request headers (`end_of_stream=false`); Envoy never forwards the upgrade upstream. Matches production. |
| ext_proc skipped for upgrades, upstream over HTTP/2 | chatgpt.com answers `400`: it rejects WebSocket-over-HTTP/2 (RFC 8441). Upgrades must go upstream over **HTTP/1.1**. |
| **`FULL_DUPLEX_STREAMED`** + HTTP/1.1 upstream | **Works.** Every frame, both directions, reaches the processor in order. Envoy forwards only what the processor streams back (it owns the bytes). ~1.5–2 ms per round trip locally. |
| **`observability_mode`** + HTTP/1.1 upstream | **Works.** Same frames, async; the processor can't act. `source.address` is still delivered (per-device attribution intact). |
| One policy on both transports (full-duplex) | **Works.** One function ("redact `secret`") rewrote a WebSocket message (decode → modify → re-encode with new length and mask) and an HTTP body; upstream received the redacted text both ways. |
| Real Codex through local full-duplex proxy | **Works.** One WebSocket (`101`, 49 s, 34 KB up / 168 KB down) carried all 5 model turns; task 51 s. Frames are **permessage-deflate compressed with context takeover**; decoded they are `response.create` (client) and the same `response.*` events as the HTTP/SSE transport (server), plus WebSocket-only `codex.rate_limits`, `codex.response.metadata`, `responsesapi.websocket_timing`; plus ping/pong. One frame spanned two body chunks. |
| Processor killed mid-WebSocket, full-duplex, `failure_mode_allow: true` | **Open connection closed immediately; new connections rejected.** |
| Two ext_proc filters (observe + enforce), each disabled per route | **Works.** Observe route passed traffic through unchanged; enforce route redacted it (WebSocket and HTTP). With the enforce processor killed, the observe route kept working and only the enforce route failed. |
| Same, `failure_mode_allow: false` | Same. Full-duplex is fail-closed regardless of the flag. |
| Same, observability mode | **No effect on traffic.** Open connection kept working; new connections fine. |

Envoy API facts that constrain the design (go-control-plane v1.40 docs):
- Full-duplex has **no message timeout** (`message_timeout` doesn't apply), so a
  stalled processor stalls the stream. Enforce needs its own deadline.
- Full-duplex requires trailer mode `SEND`, and `mode_override` is unsupported.
- Observability mode supports only `STREAMED` / `NONE` bodies; responses are
  ignored; `deferred_close_timeout` (default 5 s) bounds how long Envoy keeps
  the side stream after the request ends.
- Per-route overrides (`ExtProcPerRoute.overrides`) can change `processing_mode`
  and `failure_mode_allow`, but **not** observability mode: the per-route
  `async_mode` field is "deprecated and not implemented". Mixing modes therefore
  means two filter instances, each disabled per route (verified above).

## 3. Architecture

```
client ──TLS──► Envoy ──────────────────────────────────────────► provider
                  │  ext_proc stream (one per HTTP request or WebSocket connection)
                  ▼
               observer
                 ├─ transport decoders ─► Message stream (HTTP body │ SSE event │ WS message)
                 ├─ extractors (model, tokens, tools, identity, analytics …) ─► rows ─► sinks
                 └─ [enforce] policy hook ─► allow / modify / block ─► re-encode ─► back to Envoy
                                   │
                                   └─ external Policy service (customer-written, gRPC)
```

### 3.1 Data plane (Envoy)

- **Two ext_proc filter instances, always present**, both pointing at the
  observer:
  - `aintercept.observe`: `observability_mode: true`, bodies `STREAMED`,
    `failure_mode_allow: true`. Replaces today's synchronous `STREAMED` + 200 ms
    `message_timeout`, so HTTP traffic also stops waiting on the observer.
  - `aintercept.enforce`: `FULL_DUPLEX_STREAMED` request/response bodies,
    trailers and headers `SEND`, `failure_mode_allow: false`.
  - Every route enables exactly one of them (the other is disabled with
    `ExtProcPerRoute{disabled: true}`), per the enforce scopes in §3.7. The
    observer knows which mode a stream is in from the `observability_mode`
    flag Envoy sets on each `ProcessingRequest`, so one service serves both.
- **WebSocket upgrade routing for every intercepted host.** For each host, the
  chart generates a sibling cluster `upstream_<host>_ws` that is the same
  upstream (verified TLS, pinned resolvers) but **HTTP/1.1 only** (ALPN
  `http/1.1`), and an upgrade route ahead of the default route that matches
  `upgrade: websocket` and sends to it. Generic, not Codex-specific.
- The rendered Envoy config gains no other behavior. Header hygiene (no
  `x-envoy-*`, `Alt-Svc` stripping, …) is unchanged.

### 3.2 Connection model (observer)

One ext_proc stream = one **Connection**:

| Field | Notes |
|---|---|
| `connection_id` | today's `correlation_id` |
| `transport` | `http` or `websocket` (from the `upgrade` request header and a `101` response) |
| host, path, method, status, timing | as today |
| identity | ip, key hash, account/device/session (all existing extraction) |
| `ws_extensions` | negotiated `Sec-WebSocket-Extensions` (from the `101` response): permessage-deflate parameters |

### 3.3 Transport decoders → Messages

Every transport is reduced to the same unit:

```go
type Message struct {
    ConnectionID string
    Transport    string // http | websocket
    Direction    string // client_to_server | server_to_client
    Seq          int    // order within connection+direction
    Kind         string // http_request_body | http_response_body | sse_event | ws_text | ws_binary | ws_close
    Payload      []byte // decoded: decompressed, unmasked, reassembled
    JSON         any    // parsed when Payload is JSON
}
```

- **HTTP request body:** one Message (buffered up to the existing 4 MiB cap; decoded per `content-encoding`).
- **HTTP response:** SSE → one Message per event; JSON → one Message.
- **WebSocket**, a new decoder, per connection and direction:
  - Frame parser: header, extended lengths, masking, **reassembly across ext_proc chunks** and **fragmented messages** (continuation frames).
  - Control frames (ping/pong/close) are recorded and never block.
  - **permessage-deflate**: parameters from the `101` response; one inflater per direction; context takeover honored; `*_no_context_takeover` and `max_window_bits` respected.
  - Each complete data message → one Message.
- Bounded memory: per-message cap (default 4 MiB, like HTTP), per-connection buffer cap. Overflow marks the Connection `truncated` and stops decoding that direction; traffic is never affected.

### 3.4 Extractors (rows)

- Existing extractors are refactored to consume Messages instead of raw
  buffers. **For HTTP the output rows must be byte-for-byte identical** to
  today's (golden tests from captured traffic).
- **WebSocket model turns:** a client `response.create` opens a turn, and the
  server's `response.*` events through `response.completed` close it. Each turn
  produces one `complete` row, identical in shape to an HTTP model call (model,
  effort, tokens, `tool_uses`, `message_id`, identity, …), plus
  `transport: websocket` and the `connection_id` it rode on. The existing SSE
  parsers apply unchanged, since the events are the same JSON.
- WebSocket-only events: `codex.rate_limits` → `provider` (plan, usage
  percentages); `codex.response.metadata.headers` → `provider` (allowlisted).
  Unknown event types → schema samples (existing mechanism, keyed by event
  type).
- A WebSocket connection itself also produces one row on close (duration,
  bytes, message counts, close code, turns carried).
- No per-message rows: `output_text.delta` alone was 72 messages in one task.

### 3.5 Policy interface (enforce; dry-run in observe)

A gRPC service a third party implements; the observer is the client.

```proto
service Policy {
  // Once per connection, after request headers: allow, deny (with status), or
  // opt the connection out of further evaluation.
  rpc OnConnection(Connection) returns (ConnectionVerdict);
  // Once per Message, in order per direction.
  rpc OnMessage(Message) returns (MessageVerdict);
}
message MessageVerdict {
  oneof action { Allow allow = 1; Replace replace = 2; Drop drop = 3; Close close = 4; }
  repeated Annotation annotations = 5; // recorded on rows (e.g. "redacted:pii")
}
```

- `Message` carries the normalized payload (JSON where available) plus
  connection identity, so one policy sees HTTP and WebSocket identically.
- `Replace`: new payload; the observer **re-encodes** per transport:
  - HTTP request body: replaced whole; Envoy drops `content-length`.
  - SSE: the event is re-serialized.
  - WebSocket: new frame(s). With permessage-deflate, once any message in a
    direction is evaluated, **every** later message in that direction is
    re-compressed through the observer's own per-direction compressor, so the
    peer's inflate context stays consistent. Unmodified messages keep their
    content.
- `Drop` / `Deny`: the message or connection is rejected **the way the
  provider itself rejects it**, so clients handle it through their normal
  error paths. For OpenAI/Codex that means emulating OpenAI's own error frames
  and close behavior on the WebSocket, and its HTTP error body and status
  otherwise. Those formats are captured from real rejections (e.g. a request
  for a model the account can't use) before implementation, per provider.
- `Close`: closes the WebSocket with a close code, or resets the HTTP stream.
- **Dry-run in observe mode:** verdicts are computed (async) and recorded on
  rows, never applied. Lets a customer test policies on live traffic safely.
- A **reference policy** ships with the repo (redaction plus block-by-model or
  provider) as an example and a test fixture.

### 3.6 Failure semantics

| Failure | observe | enforce |
|---|---|---|
| Observer down / crashes | No effect on traffic (verified) | **Traffic stops**: open connections close, new ones are rejected (verified; Envoy behavior, not configurable) |
| Policy service down / slow | n/a (dry-run only; verdicts missing are recorded) | **Fail closed** (`policy.onError: block`, the decided default): the connection or message is rejected in the provider's native error format. Policy-service failures never become observer crashes |
| Policy evaluation exceeds deadline | n/a | Same: fail closed. Deadline enforced by the observer (Envoy has none in full-duplex) |
| Decoder can't parse (unknown compression, garbage) | Connection marked `undecodable`, schema sample, traffic unaffected | Same, and `policy.on_undecodable`: `allow` or `block` |

So enforce scopes require: an observer HA posture (≥3 replicas, PDB,
anti-affinity); **a Policy service with matching availability**, since its
outage now blocks enforce-scoped traffic (fail closed); observer readiness
gated on policy reachability, so a replica that can't reach the policy stops
taking enforce traffic rather than rejecting it; and the kill switch
(Cloudflare steering) plus "move the host back to observe" as the outage
recovery.

### 3.7 Configuring observe vs enforce

Two layers, because they control different things:

| Layer | Decides | Where | Changes take effect |
|---|---|---|---|
| **1. Data-plane scope** | Which traffic is **inline** at all (full-duplex, fail-closed) vs observed async | Helm values → Envoy routes | On deploy |
| **2. Policy scope** | For each inline connection: does the policy **act**, only **record** (dry-run), or **ignore** it | The customer's Policy service, via `OnConnection` | Live, per connection |

**Layer 1 sets the blast radius.** Anything in an enforce scope depends on
the observer being up (Envoy fails closed in full-duplex, §2). So it's scoped
deliberately, by host and optionally path; everything else stays observe:

```yaml
interception:
  defaultMode: observe          # every intercepted host not listed below
  enforce:                      # opt-in inline scopes
    - host: chatgpt.com
      paths: ["/backend-api/codex/responses"]   # optional; default: whole host
    - host: api.anthropic.com
  websocket:
    enabled: true               # upgrade routes + HTTP/1.1 sibling clusters per host
policy:
  endpoint: ""                  # gRPC address of the customer's Policy service
  timeout: 50ms                 # per-message deadline (enforce)
  onError: block                # policy unreachable / over deadline → fail closed
  onUndecodable: allow          # allow | block
```

The chart renders one route per (host, path) scope, with `aintercept.enforce`
enabled on enforce scopes and `aintercept.observe` everywhere else. Moving a host
from observe to enforce is a reviewed config change, like adding an
intercepted domain.

**Layer 2 is where targeting happens.** Envoy can't route by person (identity
is only known inside the observer), so per-user, per-group, per-device or
staged rollout is decided by the Policy service at connection time:

```proto
message ConnectionVerdict {
  oneof action {
    Enforce enforce = 1;   // evaluate every message; apply verdicts
    DryRun  dry_run = 2;   // evaluate; record verdicts on rows; never apply
    Bypass  bypass  = 3;   // don't evaluate; forward as-is (still recorded)
    Deny    deny    = 4;   // refuse the connection (provider-native error)
  }
}
```

On observe scopes, `OnConnection` / `OnMessage` run as dry-run regardless of
the verdict (the data plane can't act there), so a customer can trial a policy
against everyone's traffic before any host goes inline. Note that `Bypass` on
an enforce scope still keeps the connection inline (observer availability
still applies); it only skips policy evaluation.

## 4. Changes, file by file

| Area | Change |
|---|---|
| `deploy/helm/aintercept/files/envoy.multi.yaml` (+ `envoy/envoy.multi.yaml` for compose) | Both ext_proc filters (`aintercept.observe`, `aintercept.enforce`); routes templated from the allowlist and enforce scopes, each enabling one filter; per-host upgrade route + `_ws` HTTP/1.1 cluster |
| `deploy/helm/aintercept/values.yaml`, templates | `interception.defaultMode`, `interception.enforce[]`, `interception.websocket`, `policy.*`; HA settings applied when any enforce scope exists (replicas ≥3, PDB, anti-affinity, readiness on policy reachability) |
| `aintercept/internal/extproc/server.go` | Don't reply in observability mode; in enforce, stream bodies back (full-duplex); track `transport`; hand body chunks to decoders instead of buffering raw |
| `aintercept/internal/wsframe/` (new package) | Frame parser, reassembly, fragmentation, control frames, permessage-deflate inflate (+ deflate for enforce); fuzz-tested |
| `aintercept/internal/message/` (new package) | `Message` type; HTTP and SSE decoders moved here from `events` |
| `aintercept/internal/events/` | Extractors consume Messages; WebSocket turn assembly; connection row; WS-only events into `provider` |
| `aintercept/internal/policy/` (new package) + `proto/policy/v1/policy.proto` | Client for the Policy service, deadlines, on-error handling, dry-run recording, re-encoding |
| `examples/policy-reference/` | Reference policy implementation (stays inside the observer module, or a separate small module; to decide) |
| Tests | Golden HTTP rows from captured traffic (no regressions); WS decoder fuzz + captured Codex frame fixtures (synthetic content); Envoy-level integration test (the spike harness, promoted to `test/ws/`); failure drills |
| Docs | `PRODUCTION.md` (modes, failure semantics, HA); `TAXONOMY_CODEX.md` (WebSocket rows); this spec → design doc of record |

New dependencies: none expected (`compress/flate` is stdlib; the gRPC stack is
already present).

## 5. Phases and acceptance criteria

**Phase 1: observe WebSockets (removes the Codex degradation). Done.**

Results on DO (observer v17, Envoy v1.39.6), Codex 0.164.0, same task
back-to-back: proxied 13.0 s / 11.8 s vs direct 11.3 s / 12.9 s (+2.5% on
average, within run-to-run noise); each run one WebSocket (`101`), 0 HTTP
fallbacks, 5 turns per connection all emitted as rows (model, tokens incl.
cached, `tool_uses` incl. MCP calls inside `exec` scripts, identity, plan);
both observer pods deleted mid-session with no effect on Codex; Claude Code
HTTP rows unchanged; local smoke test 9/9 in observability mode. Not done
here: golden tests from captured traffic (they belong with the Phase 2
extractor refactor, where HTTP output must stay identical).

- Envoy: observability mode; upgrade routes + `_ws` clusters for all hosts.
- Observer: WebSocket decoder (inflate only) → Messages → WS turn rows and
  connection rows; HTTP path unchanged.
- Done when, on DO:
  - Codex completes over a single WebSocket (`101`, no HTTP fallback) and its
    wall time through the proxy is within 10% of direct (same task, measured
    back-to-back).
  - Every model turn on the WebSocket yields a `complete` row with model, tokens,
    `tool_uses` and identity matching what the HTTP transport produced for the
    same task.
  - The Claude Code + Codex HTTP smoke and golden tests are unchanged.
  - Killing the observer mid-session has no effect on the Codex session.

**Phase 2: message layer and policy interface (dry-run).**
- Extractors on Messages (golden tests identical); Policy proto, client,
  dry-run; reference policy.
- Done when the reference policy's verdicts are recorded on rows for both
  HTTP and WebSocket traffic in observe mode, with zero traffic impact.

**Phase 3: enforce mode.**
- First: capture each provider's native rejection behavior (OpenAI/Codex over
  WebSocket and HTTP; Anthropic over HTTP) from real rejected requests, and
  document it as the format `Drop` / `Deny` / fail-closed emulate.
- Full-duplex scopes; re-encoding (HTTP body, SSE event, WebSocket frames incl.
  re-compression); deadlines; fail-closed `onError`; `onUndecodable`; HA chart.
- Done when:
  - The reference policy redacts and blocks over HTTP and WebSocket for Claude
    Code and Codex.
  - Added latency is measured under load (target p50 < 5 ms, p99 < 25 ms per
    message excluding policy time, to confirm).
  - Failure drills (kill the observer, kill or slow the policy service,
    mid-stream) behave exactly as §3.6 states.

## 6. Open questions and risks

- **Provider-native rejection** must be captured before it can be emulated
  (none appeared in today's captures). If a provider's rejection is
  connection-closing, a per-message `Drop` may have to close the connection
  too.
- **Full-duplex maturity.** It's a recent Envoy mode, and upgrades through it
  are lightly exercised upstream. Keep the version pinned, and keep the spike
  harness as a regression test across Envoy upgrades.
- **Re-compression cost** in enforce: every message in an evaluated direction
  is re-deflated. Measure.
- **Head-of-line blocking** in enforce: both directions share one gRPC stream to
  the observer; a slow policy on one direction may delay the other.
- **Long-lived connections:** observability mode's `deferred_close_timeout`
  (5 s default) and WebSocket lifetimes of minutes or hours: verify there are no
  premature closes of the side stream.
- **Other WebSocket clients** (OpenAI Realtime, Gemini Live, web apps): the
  decoder is generic; extractors per provider come later.
