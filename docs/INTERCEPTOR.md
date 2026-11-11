# The Interceptor: plugging a control plane into AIntercept

AIntercept sees every AI call that passes through it. The **Interceptor** is
how you act on what it sees. You run a gRPC service that implements
`aintercept/interceptor/v1/interceptor.proto`. AIntercept calls it for every
intercepted call, and depending on the mode it records or applies what your
service decides.

- **One integration, two modes.** Observe and enforce use the same callbacks
  and the same messages. Moving a host from observe to enforce is a deployment
  setting, never a rewrite.
- **No SDK needed.** Generate a server from the proto in any gRPC language.
  Complete examples are in `aintercept/examples/redact/` (Go) and
  `aintercept/examples/python-redact/` (Python).
- **Building one?** [`BUILDING_AN_INTERCEPTOR.md`](BUILDING_AN_INTERCEPTOR.md)
  walks through it end to end: the code, local testing through a real Envoy,
  packaging, deployment, Helm wiring, and the observe-then-enforce rollout.

## The interface

```proto
service Interceptor {
  rpc OnCallStart(CallStart) returns (Verdict);     // the parsed request, before it goes upstream
  rpc OnMessage(Message) returns (Verdict);         // content: request body, each SSE event, each WebSocket message
  rpc OnCallComplete(CallRecord) returns (Ack);     // the normalized record of the finished call
}
```

| Callback | When | Carries | Verdicts |
|---|---|---|---|
| `OnCallStart` | The request's headers and body have arrived and been parsed. For a WebSocket, at the upgrade. | `Call` (id, mode, host, path, identity, client), the request headers with credentials redacted, and `RequestInfo`: model, effort, stream, declared tools, MCP tools, attachments, size. | Allow, Deny |
| `OnMessage` | Each unit of content, in order per direction. **Only when Interceptor content is on.** | The decoded payload (decompressed, unmasked, reassembled) and its kind: `HTTP_REQUEST_BODY`, `HTTP_RESPONSE_BODY`, `SSE_EVENT` (one event of a streamed response), `WS_TEXT` or `WS_BINARY`. | Allow, Replace, Drop, Deny |
| `OnCallComplete` | The call has ended. Always asynchronous: there's nothing left to decide. | `CallRecord`: status, the model served, input, output, cache and reasoning tokens, stop reason, tool calls by name and count, time to first byte, duration, bytes, error, and the verdicts you returned (and whether they were applied). | — |

`Call.call_id` is the same on every callback for a call, and is the
`correlation_id` on AIntercept's own records.

A **WebSocket connection is one call**: one `OnCallStart` at the upgrade,
every message through `OnMessage`, and one `CallRecord` per model turn
(`ws_turn` 1, 2, …) plus a final one when the connection closes (`ws_turn`
0).

**Identity** is what the call itself shows:
- the salted key hash and its display hint;
- the client IP;
- the provider account and org;
- the client's device and session IDs (Claude Code's `metadata.user_id`,
  Codex's turn metadata);
- the email, when the credential carries it.

AIntercept doesn't enrich it: linking accounts to people happens later, in its
processor.

### Verdicts

| Verdict | Effect in enforce mode |
|---|---|
| Allow (or no action) | Forwarded unchanged. |
| Replace{payload} | Your decoded payload is forwarded instead. AIntercept re-encodes it for the wire: it fixes `content-length` and `content-encoding`, rewrites the SSE event with its other fields (`event`, `id`) kept, and re-frames WebSocket messages (masked toward the server). |
| Drop | This SSE event or WebSocket message isn't forwarded. For a whole HTTP body it behaves like Deny. |
| Deny{reason, http_status} | The call is refused **in the provider's own error format**, so clients fail through their normal error handling. Before the response has started: the HTTP status (default 403) with the provider's error body (Anthropic `permission_error`, OpenAI `blocked_by_policy`, Gemini `PERMISSION_DENIED`). In a streamed response: the provider's `error` event, then the stream ends. On a WebSocket: the error message, then close code 1008. |

`Verdict.annotations` (key/value) are recorded with the verdict.

## Modes

|  | Observe | Enforce |
|---|---|---|
| Which traffic | Every intercepted call, by default | The hosts and paths listed in `interceptor.enforce` |
| `OnCallStart`, `OnMessage` | Queued and delivered asynchronously; traffic never waits | In the request path: Envoy waits for the verdict |
| Verdicts | Recorded as what would have happened (`applied: false`) | Applied (`applied: true`) |
| `OnCallComplete` | Async | Async |
| Interceptor slow or down | Callbacks are delayed, then dropped and counted (`/stats`). Traffic is unaffected. | After `deadlineMs` (default 50 ms), or on error: the call is refused, 503 "interceptor unavailable" (`onError: deny`, the default), or let through (`onError: allow`) |
| Observer down | Traffic is unaffected | Enforced calls are refused (Envoy fails closed) |

`Call.mode` tells your service which mode a callback is in.

**The rollout path:**
1. Run your Interceptor in observe mode against all traffic.
2. Read the recorded verdicts to see what it would have done.
3. Move specific hosts and paths to enforce.

Your code doesn't change between the steps.

## Interceptor content (separate from what AIntercept records)

`interceptor.content` decides whether `OnMessage` exists. It's independent of
`interceptionMode`, which decides what AIntercept itself records:

| | `interceptionMode: content-free` | `interceptionMode: full` |
|---|---|---|
| `interceptor.content: false` | Neither stores nor sends bodies | Records bodies; the Interceptor sees metadata only |
| `interceptor.content: true` | **The Interceptor sees bodies, AIntercept stores none** (e.g. redaction without retention) | Both |

Without content, an Interceptor can still decide on everything in
`CallStart`: model, tools, client, identity, host and path.

With content on, enforce asks upstream for responses it can evaluate and
release without re-compressing. It removes `accept-encoding` from requests, and
`Sec-WebSocket-Extensions` from WebSocket upgrades, so WebSocket compression
isn't negotiated. A response that arrives compressed anyway, or is larger than
32 MiB, is released unevaluated and counted.

## Deploying

Helm values:

```yaml
interceptor:
  endpoint: my-interceptor.policy.svc:9100   # host:port of your service
  content: true                              # OnMessage (bodies)
  deadlineMs: 50                             # enforce: per callback
  onError: deny                              # enforce: deny (fail closed) | allow
  enforce:                                   # empty: observe only
    - host: api.anthropic.com
      paths: ["/v1/messages"]                # optional; default the whole host
    - host: chatgpt.com
      paths: ["/backend-api/codex/responses"]
  tls:
    enabled: true
    secretName: interceptor-tls              # ca.crt; tls.crt + tls.key for mutual TLS
    ca: true
    clientCert: true
    serverName: my-interceptor.policy.svc
  tokenSecret: interceptor-token             # key `token`, sent as a Bearer token
```

- **Without the chart:** set the observer's environment: `INTERCEPTOR_ENDPOINT`, `INTERCEPTOR_CONTENT`,
  `INTERCEPTOR_DEADLINE_MS`, `INTERCEPTOR_ON_ERROR`, `INTERCEPTOR_TLS`,
  `INTERCEPTOR_TLS_CA_FILE`, `INTERCEPTOR_TLS_CERT_FILE`,
  `INTERCEPTOR_TLS_KEY_FILE`, `INTERCEPTOR_TLS_SERVER_NAME`, `INTERCEPTOR_TOKEN`,
  plus `INTERCEPTOR_OBSERVE_TIMEOUT_MS`, `INTERCEPTOR_QUEUE_SIZE` and
  `INTERCEPTOR_WORKERS` for observe delivery.
- **Enforce scopes need an Envoy route:** the hosts in `interceptor.enforce`
  must be in `interception.allowlist`, and the chart checks this.
- **Where enforce scopes take effect:** they're applied by the Helm chart's
  Envoy config. The local compose stack is observe only;
  `scripts/test-interceptor.sh` runs both modes.

**Running enforce:** an enforced call depends on the observer and on your
Interceptor. Run both highly available:
- at least 3 observer replicas. With enforce scopes set, the chart requires at
  least 2 (`observer.autoscaling.minReplicas`) and adds a PodDisruptionBudget;
- your service on the same footing, close to the observer (same region, same
  cluster ideally);
- latency in mind: each `OnMessage` on a streamed response is one round trip
  per event.

Start with narrow scopes (one path), and widen them once the verdicts in
observe mode look right.

## Where verdicts are recorded

Every verdict that isn't a plain Allow, plus every failure, is a row in
AIntercept's raw stream:
- the row has phase `verdict`, keyed by the call's `correlation_id`, with
  `callback`, `action`, `applied`, `mode`, `reason`, `annotations` and
  `latency_ms`;
- a refused call's complete row carries the status it was refused with and
  `error: "denied by interceptor: …"`;
- the same verdicts reach your service on the call's `CallRecord`.

The observer's `/stats` has an `interceptor` block: queued, dropped,
delivered, failed, decided, decide_failed.

## The example: redact "silly clown"

`aintercept/examples/redact` is about 90 lines of Go:
- every message has "silly clown" replaced with `[redacted]`, in both
  directions;
- calls asking for the model `forbidden-model` are refused at `OnCallStart`;
- each call's start and record is logged.

It's built into the `aintercept` image as `/redact-interceptor`:

```bash
# Next to an observer: listens on :9100
docker run --entrypoint /redact-interceptor <aintercept image>
```

`make test-interceptor` runs it in an end-to-end test through a real Envoy:
- enforced calls are redacted in both directions (event stream, JSON body and
  WebSocket);
- observed calls pass untouched, with their verdicts recorded;
- `forbidden-model` gets the provider's 403, and the denied call still gets
  its record;
- with the Interceptor stopped, enforced calls get a 503 and observed calls
  carry on.

Its matching is per message: a phrase split across two streamed text deltas
isn't caught. A real policy that needs that keeps state per `call_id`.

## Writing one in another language

Generate the stubs from the proto with your language's gRPC toolchain, for
example:

```bash
python -m grpc_tools.protoc -I aintercept --python_out=. --grpc_python_out=. aintercept/interceptor/v1/interceptor.proto
```

Implement the three methods, and return quickly from `OnCallStart` and
`OnMessage`: in enforce mode they're in the request path. For Go, import
`…/aintercept/interceptor/v1` directly (see the example). Regenerate the Go code
after changing the proto with `make proto`.
