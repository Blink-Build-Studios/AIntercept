# Interception issues log

A running record of every obstacle hit while extending coverage to new clients
(CLIs, browsers, desktop apps). Each entry gives what happened, how we know,
its status, and the fix or the candidate fixes. Add new entries at the top of
the relevant section. Status: **open**, **mitigated** (a workaround is in
place), **fixed**, or **test-only** (an artifact of how we test, not
something a deployment hits).

## Bot management / provider-side detection

### B1. Cloudflare clearance is bound to the egress IP; multi-node egress breaks it. Status: mitigated (spike)
- **What:** claude.ai sits behind Cloudflare bot management. A passed
  challenge sets `cf_clearance`, which is bound to the client IP Cloudflare
  sees. Our Envoy pods run on different nodes, and each leaves DO from its own
  node IP (203.0.113.21, 203.0.113.22). When a browser connection landed on
  the other pod, Cloudflare saw a new IP and challenged again, which looks like
  a "Just a moment..." loop.
- **Evidence:** claude.ai challenged repeatedly with two Envoy pods. It worked
  after the user re-verified with one pod.
- **Spike workaround:** KEDA paused at one Envoy replica
  (`kubectl annotate scaledobject envoy autoscaling.keda.sh/paused-replicas=1`).
  **Remove this annotation when the spike is over.**
- **Fix:** all Envoy egress through one stable IP (or a small stable set):
  a NAT gateway on AWS, a DO NAT gateway / egress gateway, or a
  Cilium/Calico egress gateway. Document this as a deployment requirement,
  and also call out IP-reputation hygiene (see B3). Session affinity alone
  isn't enough, because egress is per node.
- **Why not pin users to a pod instead:** in-cluster stickiness (Service
  `sessionAffinity: ClientIP`, IPVS source hashing, Cilium Maglev) acts after
  the cloud load balancer has already picked a node. With
  `externalTrafficPolicy: Local` that pick decides the pod, and DO's TCP load
  balancer has no stickiness. With `Cluster`, kube-proxy SNATs, so affinity
  keys on the node rather than the client. Either way scaling events reshuffle
  users. A stable egress IP fixes it for every pod count.
- **By platform:** AWS/EKS nodes in private subnets already leave through a
  NAT gateway (one IP per AZ), so B1 mostly doesn't arise there. DOKS has no
  managed NAT/egress gateway in our `doctl` (1.163) and its Cilium is managed;
  the option there is routing egress through a gateway droplet with a fixed
  IP (provisioning, your call). For the demo, one Envoy pod is fine.

### B2. Upstream TLS / HTTP/2 fingerprint doesn't match the browser. Status: fixed (mostly)
- **What:** Envoy originates the upstream TLS connection itself, so the
  provider sees Envoy's BoringSSL ClientHello (JA3/JA4) and Envoy's HTTP/2
  SETTINGS, alongside a Chrome `User-Agent`. Bot-management scoring compares
  them.
- **Evidence:** chatgpt.com (also Cloudflare) never challenged. claude.ai
  challenges some XHRs even after clearance is valid (`/api/v2/rum`,
  `/api/event_logging/v2/batch`, `/manifest.json`, some `/api/organizations/*`
  GETs get `cf-mitigated: challenge` while their neighbours return 200). That
  pattern points to a low bot score rather than an expired clearance.
- **Candidate fixes:** (a) configure Envoy's upstream TLS to look like
  Chrome: cipher-suite order, curves (X25519MLKEM768, X25519, P-256),
  signature algorithms, ALPN, and HTTP/2 SETTINGS/window sizes. We can't
  configure GREASE or extension order, so this narrows the gap but doesn't
  close it. (b) Mirror the client's fingerprint per connection: the
  listener already sees the client's ClientHello, so an upstream TLS stack
  that replays it would make the fingerprint match. That needs a custom
  upstream (e.g. a Go uTLS sidecar or Envoy extension), which is a new
  component, so it needs sign-off. (c) The customer allowlists their egress
  IPs with the provider's enterprise program, where one exists.
- **Measured** (tls.peet.ws through a local Envoy with our cluster config):
  | | JA4 | HTTP/2 (Akamai) |
  |---|---|---|
  | Chrome 156 | `t13d1517h2_8daaf6152771_cb7bf5808d99` | `1:65536;2:0;4:6291456;6:262144\|15663105\|0\|m,a,s,p` |
  | Envoy, stock | `t12d68h2_39a52dd9ae43_0f3b2bcde21d` (**TLS 1.2 only**, 6 ciphers) | `2:0;1:4096;8:0;3:1024;4:16777216\|25100289\|0\|a,p,m,s` |
  | Envoy, fixed | `t13d1511h2_8daaf6152771_9ced094328c9` | `2:0;1:65536;8:0;3:1024;4:6291456\|15663105\|0\|a,p,m,s` |
- **Fixed:** every upstream cluster now uses Chrome's TLS parameters (TLS 1.3,
  Chrome's cipher list and order, X25519MLKEM768/X25519/P-256/P-384, Chrome's
  signature algorithms; anchor `upstream_tls_params`) and Chrome's HTTP/2
  HPACK table and window sizes. The JA4 cipher hash now matches Chrome
  exactly. Remaining differences can't be configured in Envoy: GREASE, the
  browser-only extensions (ALPS, SCT, OCSP status_request, certificate
  compression, ECH GREASE; 11 vs 17 extensions), and pseudo-header order.
- **Result:** claude.ai responses carrying `cf-mitigated: challenge` fell from
  about 25% of a session (e.g. 227 of 853 in one minute) to 3 of 281 in the
  first session after the fix. Scripted full page loads on claude.ai
  (which reliably re-triggered the challenge before, see B4) loaded with no
  challenge, and a fresh browser launch reached claude.ai unchallenged.
- **If needed later:** option (b), replaying the client's own ClientHello
  upstream, would close the remaining gap but needs a new component.

### B3. Our own path probe hurts the egress IP's reputation. Status: fixed
- **What:** every observer pod probes every allowlisted host through Envoy
  every 15s (`GET /`, no cookies, UA `aintercept-proxyprobe`). With 7 observer
  pods that's about 28 cookieless requests a minute to claude.ai from the same
  egress IP as real users, which is exactly what a bot looks like.
- **Evidence:** a steady 8-28 `302`s a minute on `claude.ai GET /` in the
  captures, all from the probe.
- **Spike workaround:** `observer.pathProbe.interval=300s` on DO.
- **Fix:** probe from one elected instance rather than every pod; probe web
  hosts (claude.ai, chatgpt.com) at most every few minutes, or check
  TLS + upstream connectivity only (handshake through Envoy plus a cheap
  upstream request to an API host on the same provider); make the probe path
  configurable per host. A probe must never be the loudest client on the IP.
- **Fixed:** probe targets are configurable per allowlist entry
  (`observer.pathProbe.targets`, `entry=host/path`). Cloudflare-fronted web
  hosts (chatgpt.com, ab/ws.chatgpt.com, claude.ai, a.claude.ai) probe
  `/cdn-cgi/trace`, which Cloudflare's edge answers without loading the app:
  it still proves TLS, Envoy, the upstream hop and the real edge. The spike
  interval override is removed (back to 15s). Still possible later: probing
  from one elected replica.

### B6. claude.ai re-checks after idle. Status: open (expected behavior, watch)
- After about 20 minutes idle, the claude.ai tab showed Cloudflare's
  interactive check again, with the same Envoy pod and egress IP. This matches
  Cloudflare's challenge-passage lifetime (often 30 minutes) rather than a proxy
  fault; a direct (unproxied) user can see the same thing. **Watch:** whether
  the frequency through the proxy is higher than direct. If it is, the
  remaining fingerprint gap (B2) is the lever.

### B5. Test traffic drains the test account's free-plan quotas. Status: test-only
- The ChatGPT free plan allows a handful of "reason", "file_upload" and
  "image_gen" uses per window. Our test runs used them up (visible in
  `web_chat.limits`). Use a paid test account for longer runs.

### B4. Driving the browser through CDP re-triggers the challenge. Status: resolved (was B2)
- **What:** a full page load issued over the Chrome DevTools Protocol
  (`page.goto` via Playwright `connectOverCDP`) on claude.ai sent the tab back
  to the challenge. In-app navigation (clicking links) doesn't. An artifact
  turn that opened `?artifact=` also hit a challenge, which is B2 or B4.
- **Workaround:** the test harness navigates in-app only. A real user's
  browser has no CDP attached, so this isn't a deployment issue, but it limits
  how much we can automate testing.
- **Update:** after the B2 fix, the same scripted page loads pass. The
  trigger was the fingerprint, not CDP.

## Client / browser behavior

### C1. File uploads go to per-region wildcard hosts. Status: fixed
- ChatGPT web: `POST chatgpt.com/backend-api/files` (metadata) → `PUT
  sdmntpr<region>.oaiusercontent.com/files/<id>/raw` (bytes, SAS URL) →
  `POST /backend-api/files/process_upload_stream`. The CSP also allows
  `*.blob.core.windows.net`, so other upload paths may use Azure Blob
  directly (not seen yet).
- **Fix:** wildcard allowlist entries (`*.oaiusercontent.com`): certsvc mints
  a wildcard leaf, Envoy matches the wildcard SNI, and a dynamic
  forward-proxy cluster takes the upstream SNI and SAN check from
  `:authority`.
- claude.ai: `POST /api/organizations/<org>/conversations/<id>/wiggle/upload-file`
  (multipart) on claude.ai itself; nothing extra needed.

### C2. Extra per-client hosts appear only in a real session. Status: fixed for what we've seen
- ChatGPT web uses `ws.chatgpt.com` (per-user WebSocket, conversation resume /
  stream status) besides chatgpt.com. Codex uses `ab.chatgpt.com` (OTel
  metrics). Neither shows up in documentation; we found both from Chrome's
  NetLog and the CONNECT logs.
- **Process:** every new client gets a discovery run with full host logging
  (Chrome `--log-net-log`, the CONNECT-log proxy for CLIs) before we call it
  covered.
- Seen but not intercepted (by decision so far): `a.claude.ai` (Claude web
  analytics), `bzr.openai.com`, `browser-intake-us5-datadoghq.com`, static CDNs
  (`cdn.oaistatic.com`, `persistent.oaistatic.com`, `assets-proxy.anthropic.com`,
  `s-cdn.anthropic.com`, `images.openai.com`, `cdn.openai.com`).
- **Now intercepted:** `a.claude.ai` (Claude web's Segment analytics iframe,
  `isolated-segment.html`) and `bzr.openai.com` (ChatGPT ads/attribution
  sync, `/v1/obi/sync`). Captured; not parsed yet.
- **Opt-in only:** `browser-intake-us5-datadoghq.com`, where ChatGPT web sends its
  Datadog RUM (about 1,280 requests in one session). It's a third-party host
  shared by every site using Datadog, so intercepting it is a customer
  decision; add it to the allowlist to get it (claude.ai's RUM is first-party
  and already parsed, see X2).

### C3. QUIC / HTTP/3. Status: mitigated for proxied hosts; deployment requirement
- Browsers try UDP 443 when the server advertises `alt-svc: h3`. Envoy
  strips `alt-svc` from intercepted responses, so a browser that reached us
  once stays on TCP. A browser that learned h3 elsewhere, or from DNS HTTPS
  records, can still try QUIC first.
- **Deployment requirement:** the network path must block UDP 443 to
  intercepted hosts (the browser then falls back to TCP), or browsers run
  with QUIC disabled by policy (`QuicAllowed=false`). The spike used
  `--disable-quic`.

### C4. Encrypted Client Hello (ECH). Status: not active today; proxy-side fallback in place (untested end to end)
- chatgpt.com and claude.ai are behind Cloudflare, which offers ECH. With
  ECH the outer SNI is `cloudflare-ech.com`, so SNI-based routing can't pick
  the real host. The spike avoided it (Chrome's `--host-resolver-rules`
  bypasses DNS HTTPS records), so we haven't seen it fail yet.
- **Deployment requirement:** disable ECH by browser policy
  (`EncryptedClientHelloEnabled=false` in Chrome/Edge; `network.dns.echconfig.enabled=false`
  in Firefox) or have managed DNS strip HTTPS/SVCB records for intercepted
  hosts. **Test:** run a session with ECH on and real DNS to confirm the
  failure mode.
- **Checked:** none of the intercepted hosts publish an ECH config today.
  Their HTTPS DNS records carry `alpn` and IP hints but no `ech` key
  (chatgpt.com, ws.chatgpt.com, claude.ai, api.anthropic.com). So browsers
  send the real SNI.
- **Proxy-side fallback added:** `cloudflare-ech.com` (Cloudflare's ECH
  public name) is an intercepted name. When ECH is on, the outer
  ClientHello carries that name; we complete the handshake as it, with no
  ECH retry configs, and per RFC 9849 the browser then retries without ECH,
  sending the real hostname. This is the middlebox behavior the spec
  defines, so it should work without browser policy, but it can't be tested
  until a zone turns ECH on. Keep the browser policy as belt and braces.

### C6. Model calls can bypass a DNS-override over IPv6. Status: fixed and verified on IPv6; deployment requirement (`PRODUCTION.md` §2a)
- **What we saw:** in two windows (2026-11-03 00:00–00:57 and 15:00–17:59
  UTC), Claude Code's own analytics reported about 94 model calls
  (`tengu_api_success`, with `gzipSkipReason: custom_ca`). The proxy saw
  **no `/v1/messages` request at all** in those windows, while the same
  session's analytics and OAuth calls to the same host came through. Every
  other hour matches: hour 21 has 89 model-call rows against 90
  `tengu_api_success` events.
- **Likely cause:** the test machine routes with an `/etc/hosts` A record for
  `api.anthropic.com`. That doesn't touch the AAAA record (`2607:6bc0::10`),
  which macOS still resolves. On an IPv6-capable network, Claude Code's Node
  runtime can connect to Anthropic directly over IPv6. Its analytics go out
  through a different runtime (`Bun/1.4.4` user agent) that stayed on IPv4.
  The gap windows came from a different client IP (`203.0.113.93`; real IPs replaced with documentation-range ones) than the
  working hours (`203.0.113.15`), which fits a network change. On the current
  network there is no IPv6 route and everything goes through the proxy.
- **Requirement:** DNS-based routing must override AAAA as well as A records
  (or answer AAAA with nothing), or IPv6 egress must be blocked for routed
  hosts. Without that, interception silently depends on the network.
- **Detection:** comparing Claude Code's own `tengu_api_success` events with
  captured model calls (they join by `requestId`) shows this kind of bypass
  directly. That check belongs in the product UI.
- **Fix:** `scripts/route-claude-through-proxy.sh` now writes an IPv6 entry
  (`::ffff:<proxy IPv4>`, IPv4-mapped, so the load balancer needs no IPv6)
  next to each IPv4 one, and `setup` / `status` fail if any routed host still
  resolves to a non-proxy address. Node completes TLS to the proxy through the
  mapped address. The handoff and test docs give both lines; `PRODUCTION.md`
  §2a covers corporate DNS (answer AAAA with the proxy or NODATA) and an IPv6
  egress block.
- **Verified** 2026-11-03 on an IPv6-capable network (global IPv6 address and
  route; `ipv6.google.com` reachable): `api.anthropic.com` resolved only to
  the proxy, and the proxy captured 5 of 5 model calls Claude Code reported
  in the window, from that network's client IP.

### C5. Firefox has its own trust store. Status: open, deployment requirement
- Needs enterprise policy `Certificates.ImportEnterpriseRoots=true` (or the
  root installed into Firefox's NSS store). Not tested yet.

## Proxy / platform

### P1. Envoy silently serves the wrong certificate when an SDS secret is missing. Status: fixed
- **What:** Envoy (re)started while certsvc's allowlist didn't yet include
  claude.ai. The claude.ai SDS secret never arrived, and Envoy served the
  api.anthropic.com leaf for SNI claude.ai (the client failed verification).
  Restarting Envoy after certsvc had the host fixed it.
- **Fix:** (a) roll out certsvc before Envoy on an allowlist change (Helm
  hook or a checksum annotation that restarts Envoy when the allowlist
  changes); (b) the path probe already verifies the served leaf per host;
  make sure `/healthz/path` going red on this is alerting, not just
  observable; (c) consider failing the handshake rather than falling back
  to another host's cert.
- **Fixed:** SDS `initial_fetch_timeout: 0s`, so Envoy's listener (and
  `/ready`) waits for every host's leaf. A new pod stays not-ready until
  certsvc serves all its hosts, and the rolling update keeps the old pods
  serving. Verified on DO adding three hosts at once: the new pod became ready
  only after the new certsvc, and every SNI got its own leaf.

### P2. The path probe can't check wildcard entries. Status: fixed
- Wildcard allowlist entries have no single host to probe, so the probe
  skips them (`*.oaiusercontent.com`). **Fix:** per-wildcard probe host
  config (e.g. `*.oaiusercontent.com` → `sdmntprcentralus.oaiusercontent.com`).
- **Fixed** with probe targets (B3): `*.oaiusercontent.com=sdmntprcentralus.oaiusercontent.com/`.

### P4. WebSocket memory has no global budget. Status: measured and balanced; spill tier open
- Each WebSocket direction buffers up to one message (4 MiB cap: its
  fragments plus the frame being read; oversize messages are now skipped,
  not buffered), plus the turn's request (up to 4 MiB) until it completes.
  WebSockets live much longer than HTTP requests, so many idle connections
  each holding a near-cap partial message add up beyond the per-stream
  budget `MaxConcurrentStreams` assumes. **Fix:** a byte budget across all
  connections (drop decoding for the newest when exceeded), and/or a
  partial-message idle timeout.
- **Not only WebSockets:** the HTTP path had the same gap. The 2048-stream cap
  × 4.5 MiB per stream is about 9 GiB against a 256 MiB pod. A stream count
  can't bound bytes.
- **Done:** a per-pod memory budget measures every buffered byte (HTTP,
  WebSocket, queued for extraction), reported on `/stats`. The observer now
  runs in each Envoy pod (one ingest stack per pod, scaled with Envoy), so the
  earlier ORCA weighting across observer pods is gone. See
  `docs/PRODUCTION.md` §5a.
- **Decided against:** shedding (it loses telemetry, and in enforce mode
  checks), and 1s ORCA reporting (load builds over minutes; grpc-go's 30s
  minimum suffices).
- **Done:** the writer's on-disk spool, with the drop paths removed: a full
  event buffer waits, and only a full spool drops (observe mode, counted) or
  refuses (enforce mode). See `docs/PRODUCTION.md` §5a. For enforce mode (Phase 3): keep the
  policy path's memory independent of telemetry buffers, and when truly
  saturated, slow streams (flow control) and refuse new connections cleanly
  (Envoy overload manager) rather than bypass or break them.

### P5. The query API was evicted for memory at 2.2 GB on 60 MB of tables. Status: fixed
- **What:** on DOKS (two nodes, 3 GB each) the kubelet evicted the API pod
  over and over: the node ran short and the API was the pod furthest over its
  request (1.5 Gi requested, 2.5 Gi limit, DuckDB allowed 2 GB). The
  processor, also over its request (128 Mi requested, about 450 Mi used), went
  with it.
- **Why it grew:**
  - Its tables were held in memory, where DuckDB doesn't compress: 60 MB of
    Parquet became 1.1 GB.
  - 94% of the `requests` rows (900k of 966k) were the observers' path probe
    (B3): every pod, every allowlisted host, every 15 s, about 125k rows a day
    with no users at all. The probe also made the processor commit a run
    every 90 s.
  - Each commit inserts parts and swaps snapshots, and every 64 runs a
    compaction deletes and reloads the day. Replaying 400 runs (about 10
    hours) against the real image under Linux took it from 1.35 GB to
    1.58 GB, peaking at 1.84 GB, while the data grew 5%.
- **Fixed:**
  - The API keeps its tables in a DuckDB file on the pod's disk
    (`api.dbSizeLimit`), compressed and paged under DuckDB's limit.
  - It doesn't load the probe's rows, only when the probe started (the setup
    screen's "waiting since").
  - DuckDB's limit follows the container's (60% by default).
  - Requests match working sizes: API request = limit (1.5 Gi), so it is the
    last evicted; processor request 512 Mi.
  - The same replay holds at 0.41 to 0.48 GB, peaking at 0.88 GB on the first
    load. `api/tests/test_store_memory.py` guards it.
- **The processor too:** after a rebuild from raw, its first compaction read
  every run part into memory at once (three weeks of rows: 2.6 GB against a
  1 GiB limit), so it was OOM-killed on every start. Compaction now works a day
  at a time, streaming the run parts (about 250 MB live on the same data);
  `TestCompactionAfterARebuild` covers a rebuild compacted afterwards.
- **Open:** the processor still writes the probe's rows (the bucket keeps
    them; the API skips them). Probing from one elected observer (B3) would
    cut them by the pod count.

### P6. Responses over 512 KB lost their usage. Status: fixed
- **What:** the observer keeps the first 512 KB of a response to parse it. A
  streamed response's output usage and stop reason come in its last events
  (Codex's `response.completed`, Anthropic's `message_delta`), so a response
  over the cap kept its bytes and timing but lost its token counts. On the DO
  cluster: 22 of 270 Codex HTTP calls (8%, and the largest ones), 1 of 7,798
  Claude Code calls. A WebSocket turn buffers per turn and skips `.delta`
  frames, so a long-lived connection was fine; a single turn whose completed
  items passed 512 KB would have lost its `response.completed` the same way.
- **Fixed:** past the cap the observer keeps watching the stream at event
  boundaries and keeps the final events whole (up to 8 MiB each,
  `internal/extproc/tail.go`); on a WebSocket the final frame is kept past the
  cap. `tail_test.go` covers a Codex response, an Anthropic response and a
  WebSocket turn, each past the cap. Calls captured before the fix stay without
  usage (the API says why).

### P7. Requests after an idle period fail in ~2 ms with `503 UC` behind a NAT gateway. Status: fixed
- **What:** on EKS (nodes egress through AWS NAT gateways), the first
  requests after a quiet spell failed with `503 UC` ("upstream connect error
  or disconnect/reset before headers. reset reason: connection termination"),
  often several in a row, each pod failing about once per pooled upstream
  connection. Successful calls took 27-75 ms; every failure took 2 ms.
- **Why:** a 2 ms failure never reached the provider; the reset came from
  inside the VPC. An AWS NAT gateway expires a TCP flow after 350 s idle and
  answers the next packet on it with a RST. Envoy's provider clusters had no
  upstream idle timeout, so it kept pooled connections open past that and
  reused one the NAT had already forgotten.
- **Evidence (from one deployment):**
  - per-connection idle under 350 s: 0 failures in 36 requests; idle about
    420 s, 360 s, and ~30 min: failures every time;
  - Envoy counters: every failure was `upstream_cx_destroy_remote_with_active_rq`
    with `upstream_rq_pending_failure_eject`; `ssl.handshake` showed no TLS
    errors (it isn't the fingerprint, B2);
  - the same Envoy config run from a network with no NAT, idle 90 s between
    bursts, never failed (48/48).
- **Fixed:** every provider cluster sets
  `common_http_protocol_options.idle_timeout: 60s`, so Envoy closes an
  unused upstream connection long before a NAT expires it, and TCP keepalive
  (`keepalive_time: 60`, every 20 s, 3 probes), so a connection that stays
  open but quiet (a long streaming turn) keeps its NAT flow alive. Applies to
  any stateful NAT or firewall between Envoy and the providers, not only AWS.

### P3. Captures redact the `authorization` header, which hides browser identity. Status: fixed
- ChatGPT web (and Codex) authenticate with a bearer JWT whose claims carry
  the user's email, user ID and plan. We keep only `key_hash`/`key_hint`, so
  rows have the account ID but not the person. **Fixed:** the observer
  decodes a JWT bearer token's claims (unverified, for attribution) into
  `auth` on every row: email, name, user/account ids, plan, issuer, client
  id, session. The token itself is never stored. This covers Codex too.
  Also, ChatGPT web's Statsig analytics carry `user.email` on every event
  (`client_event.email`).
- **claude.ai (fixed):** it authenticates with an opaque `sessionKey`
  cookie. That cookie (and ChatGPT's session cookie, when no bearer is sent)
  now feeds `key_hash`/`key_hint`, so every row of a browser session shares
  one stable key. The account itself (`email`, `name`, account `uuid`,
  `tagged_id`) is set as `auth` on claude.ai's account responses
  (`/api/account`, `/edge-api/bootstrap/<org>/app_start`, the login
  response), read as a stream so the large bootstrap body still parses
  when the capture cap truncates it. Join any claude.ai row to its person by
  `key_hash`.

## Parsing gaps

### X1. ChatGPT web conversation stream isn't parsed. Status: fixed
- `POST /backend-api/f/conversation` returns gzip SSE in OpenAI's
  `delta_encoding: v1` format (JSON-patch-like `add`/`append`/`replace`/`patch`
  ops on a message tree), not the Responses API. Rows today carry
  `model: "auto"` only. The stream has `server_ste_metadata` (real
  `model_slug`, `plan_type`, `tool_name`, `is_search`,
  `search_tool_call_count`, `did_auto_switch_to_reasoning`, region,
  `server_ttfvt_ms`), message authors/recipients (tool calls such as
  `web.run`, `functions.exec`, image generation), titles, ads and
  `conversation_detail_metadata.limits_progress`. No token counts are
  exposed.
- **Fixed:** `web_chat` on the row (conversation id, requested vs. served
  model, plan, surface, trigger, search calls, title, limits, full
  `server_metadata`), plus `model` = the served `model_slug`, `tool_uses`
  from tool messages (each message once), `attachments` from
  `messages[].metadata.attachments`, and message linkage. Uploads
  (`POST /backend-api/files`) get `web_chat.upload`. ChatGPT web analytics
  are parsed as source `chatgpt_web`: Statsig + structured product events,
  turn logs, and counters/histograms (90 event names registered).

### X2. claude.ai completion is partly parsed. Status: fixed
- The stream is Anthropic Messages SSE plus extras, and the existing parser
  already gets the model, tool uses, stop reason and message ID. Missing:
  request `effort` / `thinking_mode`, conversation and org IDs (in the
  path), `files` / upload metadata, `message_limit` utilization (the closest
  thing to usage the web stream has), and claude.ai's own
  `/api/event_logging/v2/batch` and `/api/v2/rum` analytics.
- **Fixed:** `web_chat` (org, conversation, requested model, thinking mode,
  `message_limit` state), `effort`, attachments (`files` by reference plus
  client-extracted `attachments`), parent message linkage, and upload rows
  (`wiggle/upload-file` → `web_chat.upload`). Claude web analytics are now
  their own source `claude_web` (previously misfiled as `claude_code`; the
  registry script reassigns old rows by host); 35 event names registered.
- **RUM (fixed):** `/api/v2/rum` (Datadog browser RUM, plain or zlib NDJSON)
  is parsed into `claude_web` events named `view`, `action:<type>`,
  `resource:<type>`, `error`, `long_task`, `vital:<name>`, with session and
  user ids. **Volume note:** one session produced about 10k RUM events, about
  7.3k of them `resource:js` asset loads. They're kept per experimentation mode;
  revisit (sample or roll up static-asset resources) before production.
