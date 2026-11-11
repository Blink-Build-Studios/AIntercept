# Building an Interceptor, and wiring it into a deployment

This guide takes you from an empty directory to an Interceptor that is running
next to AIntercept, observing every AI call, and then enforcing a policy on
the calls you choose. It assumes you know gRPC and Kubernetes. It doesn't
assume anything about AIntercept beyond what's here.

[`INTERCEPTOR.md`](INTERCEPTOR.md) is the reference: the interface, the modes
and the settings. This document is the walkthrough. Read the reference's first
two sections first.

**Contents**
1. [What you're building](#1-what-youre-building)
2. [Decide what your Interceptor needs](#2-decide-what-your-interceptor-needs)
3. [Get the contract and generate code](#3-get-the-contract-and-generate-code)
4. [Implement the three callbacks](#4-implement-the-three-callbacks)
5. [Test it locally, through a real Envoy](#5-test-it-locally-through-a-real-envoy)
6. [Package it](#6-package-it)
7. [Deploy it next to AIntercept](#7-deploy-it-next-to-aintercept)
8. [Wire it in: Helm values](#8-wire-it-in-helm-values)
9. [Run in observe mode and read the verdicts](#9-run-in-observe-mode-and-read-the-verdicts)
10. [Turn on enforce](#10-turn-on-enforce)
11. [Operating it](#11-operating-it)
12. [Troubleshooting](#12-troubleshooting)
13. [Checklist](#13-checklist)

---

## 1. What you're building

```
laptop ──TLS──> Envoy ─────────────────────────────────────────────> api.anthropic.com, chatgpt.com, …
                  │ ext_proc
                  ▼
               AIntercept observer ──gRPC (client)──> YOUR INTERCEPTOR (server)
                  │                                      OnCallStart(CallStart)   → Verdict
                  ▼                                      OnMessage(Message)       → Verdict
               object store, processor, API, UI          OnCallComplete(CallRecord) → Ack
```

You write a **gRPC server**. AIntercept's observer is the **client**: it dials
your service and calls it for every intercepted call. You never talk to Envoy,
and you never handle TLS interception, certificates or provider protocols.
AIntercept parses all of that and hands you normalized messages.

There are three callbacks per call:

| | What you get | What you return |
|---|---|---|
| `OnCallStart` | The parsed request, before it goes upstream: who (key hash, IP, account, device, session), which client (Claude Code, Codex, …), which model, effort, declared tools | Allow, or Deny |
| `OnMessage` | The content, one unit at a time: the request body, each streamed event of the response, a whole JSON response, each WebSocket message. **Only if content is enabled.** | Allow, Replace, Drop, or Deny |
| `OnCallComplete` | The finished call: status, tokens, tools used, timings, error, and the verdicts you returned | Ack |

And two modes, with the same callbacks and the same messages:

- **Observe** (all traffic): your answers are recorded as what would have
  happened. AIntercept never waits for you, and nothing you do or fail to do
  affects traffic.
- **Enforce** (only the hosts and paths the deployment lists): AIntercept waits
  for your answer and applies it.

You write the code once. Which traffic is enforced is deployment configuration.

---

## 2. Decide what your Interceptor needs

Two questions determine the deployment settings, and how careful your code has
to be.

**Does it need content?**

| You want to… | Content? | You'll implement |
|---|---|---|
| Alert on, block or count by model, client, person/key, host or path | No | `OnCallStart`, `OnCallComplete` |
| Track spend or usage in real time | No | `OnCallComplete` |
| Redact secrets or PII in prompts or responses | **Yes** | `OnMessage` (Replace) |
| Block prompts that match a pattern or classifier | **Yes** | `OnMessage` (Deny) |
| Block dangerous tool calls the model makes | **Yes** | `OnMessage` on server-to-client events (Drop or Deny) |

Content is a deployment setting (`interceptor.content`). It's independent of
whether AIntercept *records* bodies (`interceptionMode`). With content on, your
service receives every prompt and response, so treat it as data that is as
sensitive as the traffic itself.

**Does it need to enforce, or only observe?**

- **Only observe** (alerting, audit, spend): latency doesn't matter. Your
  service can be a single replica, can call slow systems (Slack, a SIEM, a
  database), and can restart freely.
- **Enforce** (redacting, blocking): `OnCallStart` and `OnMessage` are in the
  request path, with a deadline of 50 ms per callback by default. A slow or
  unavailable service means refused calls. You'll need multiple replicas, low
  latency, and no slow I/O inside those two handlers.

Whatever you decide, **build it as if it will enforce**: answer fast, and keep
the slow work in `OnCallComplete` or in the background. Then turning enforce on
later is only a configuration change.

---

## 3. Get the contract and generate code

The contract is one file: `aintercept/interceptor/v1/interceptor.proto`
(package `aintercept.interceptor.v1`). Copy it into your project, and pin the
version you copied: v1 only grows by adding fields and enum values, never by
changing or removing them, so your copy keeps working. Handle enum values you
don't recognize as "unspecified".

### Go

Import the generated package directly. No codegen is needed:

```go
import pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
```

To vendor the proto into your own module instead:

```bash
protoc -I . --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative interceptor/v1/interceptor.proto
```

### Python

```bash
pip install grpcio==1.71.0 grpcio-tools==1.71.0 protobuf
python -m grpc_tools.protoc -I proto --python_out=. --grpc_python_out=. proto/interceptor/v1/interceptor.proto
touch interceptor/__init__.py interceptor/v1/__init__.py
```

That gives you `interceptor/v1/interceptor_pb2.py` (messages) and
`interceptor_pb2_grpc.py` (the servicer base class).

### Node / TypeScript

Either load the proto at runtime with `@grpc/proto-loader` and `@grpc/grpc-js`,
or generate types with `grpc-tools` or `ts-proto`. The service name is
`aintercept.interceptor.v1.Interceptor`.

### Anything else

Any language with gRPC support works. Generate a server for
`aintercept.interceptor.v1.Interceptor` with that language's `protoc` plugin.

---

## 4. Implement the three callbacks

Two complete examples live in the repo, and both pass the end-to-end suite in
section 5:

- `aintercept/examples/redact/main.go` (Go)
- `aintercept/examples/python-redact/server.py` (Python)

They do the same thing: redact "silly clown" in both directions, refuse the
model `forbidden-model`, and log every call. Here is the Python one, in full:

```python
import logging, os, re
from concurrent import futures
import grpc
from interceptor.v1 import interceptor_pb2 as pb
from interceptor.v1 import interceptor_pb2_grpc as pb_grpc

PHRASE = re.compile(rb"(?i)silly clown")
TOKEN = os.environ.get("INTERCEPTOR_TOKEN", "")

class Interceptor(pb_grpc.InterceptorServicer):
    def _authorized(self, context) -> bool:
        if not TOKEN:
            return True
        return dict(context.invocation_metadata()).get("authorization") == f"Bearer {TOKEN}"

    def OnCallStart(self, request, context):
        if not self._authorized(context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "bad token")
        if request.request.model == "forbidden-model":
            return pb.Verdict(deny=pb.Deny(reason="forbidden-model isn't approved for use here."))
        return pb.Verdict()                      # no action: Allow

    def OnMessage(self, request, context):
        if not self._authorized(context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "bad token")
        new, n = PHRASE.subn(b"[redacted]", request.payload)
        if n == 0:
            return pb.Verdict()
        return pb.Verdict(replace=pb.Replace(payload=new),
                          annotations=[pb.Annotation(key="redacted", value=str(n))])

    def OnCallComplete(self, request, context):
        r = request.response
        logging.info("complete %s status=%d in=%d out=%d", request.call.call_id,
                     r.status, r.input_tokens, r.output_tokens)
        return pb.Ack()

server = grpc.server(futures.ThreadPoolExecutor(max_workers=32))
pb_grpc.add_InterceptorServicer_to_server(Interceptor(), server)
server.add_insecure_port(os.environ.get("LISTEN", "[::]:9100"))
server.start(); server.wait_for_termination()
```

The rest of this section is what you need to know to write your own.

### 4.1 Every callback carries `Call`

| Field | Use it for |
|---|---|
| `call_id` | The key for any per-call state you keep. It's the same on every callback for the call, and is AIntercept's `correlation_id`. |
| `mode` | `MODE_OBSERVE` or `MODE_ENFORCE`: whether your verdict will be applied. Usually you return the same verdict either way; the mode is for your own logs and metrics. |
| `transport` | `HTTP` or `WEBSOCKET`. |
| `host`, `method`, `path` | The request as the client sent it, including the query string. |
| `identity` | `key_hash` (a salted hash of the API key or token; stable per credential, never reversible) and `key_hint` (e.g. `sk-ant…a3f9`), plus `ip`, `account_id`, `org_id`, `device_id`, `session_id` and `email`. Only what the call itself shows: AIntercept doesn't look anything up. |
| `client` | `product` (`claude_code`, `codex`, or empty), `user_agent`, and the client's identifying headers (`originator`, `x-app`, `anthropic-version`, …). |

To map identities to people or teams, keep your own table keyed on
`key_hash`, `account_id` or `email`, and look it up in memory.

### 4.2 `OnCallStart`

- **When it fires:** once per call, after the request headers *and* body have
  arrived and been parsed. In enforce mode nothing has gone upstream yet. For
  a WebSocket it fires at the upgrade request, before any message.
- **What it carries:** `CallStart.request` has `model`, `effort`, `stream`,
  `max_tokens`, `tools_available` (tool names), `mcp_tools_available`
  (`server/tool`), `prewarm`, `attachments` and `request_bytes`.
  `CallStart.headers` are the request headers with credentials redacted.
- **What you can return:** **Allow** (or an empty `Verdict`), or **Deny**.
  Replace and Drop make no sense here and are treated as Allow.
- **What Deny does:** the client gets the provider's own error: HTTP status
  `http_status` (default 403), with the provider's error body
  (Anthropic `permission_error`, OpenAI `blocked_by_policy`, Gemini
  `PERMISSION_DENIED`) and your `reason` as the message. Claude Code, Codex and
  the SDKs show that message to the user.

### 4.3 `OnMessage` (only with content enabled)

| `kind` | `payload` is | Direction | Notes |
|---|---|---|---|
| `HTTP_REQUEST_BODY` | The whole request body, decompressed (usually JSON: the prompt, the history, tool results) | client to server | Exactly one per HTTP call that has a body |
| `SSE_EVENT` | One server-sent event's `data` (usually JSON); `sse_event` is the event name | server to client | One per event of a streamed response, in order |
| `HTTP_RESPONSE_BODY` | A whole non-streamed response body, decompressed | server to client | AIntercept holds the response until you answer |
| `WS_TEXT` / `WS_BINARY` | One WebSocket message, reassembled and unmasked | either | Codex sends its turns over a WebSocket: `response.create` client to server, the `response.*` events back |

`seq` numbers the messages from 1, per direction.

**Replace:**
- **Return valid content of the same kind.** The client parses what you send.
  If you replace a JSON body, keep it valid JSON. The safe pattern is to parse,
  change the strings, and re-serialize, or to substitute text inside JSON
  strings with a replacement that contains no `"` or `\`.
- **AIntercept takes care of the wire:** it fixes `content-length` and
  `content-encoding`, rewrites the SSE event with its other fields (`event`,
  `id`) kept, and re-frames WebSocket messages.

**Drop:**
- **For SSE events and WebSocket messages:** the message isn't forwarded.
  Dropping a model's text delta removes those words.
- **Be careful with structural events:** dropping `message_start` or
  `response.completed` breaks the client.
- **For a whole HTTP body:** Drop behaves like Deny.

**Deny:**
- **On the request body:** the call is refused, exactly as in `OnCallStart`.
- **On a streamed response:** AIntercept sends the provider's `error` event and
  ends the stream. The client sees an error mid-answer.
- **On a WebSocket:** the client gets the provider's error message, and both
  sides get a close frame (1008).

**Streamed text is split.** A model's answer arrives as many small deltas
(`content_block_delta`, `response.output_text.delta`), so a phrase can span two
events. To catch split phrases in responses, keep a short tail of recent text
per `call_id`, and match across it. Prompts are never split: the request body
is always whole.

**Order and concurrency:**
- **Enforce:** within one call and direction, messages arrive one at a time, in
  order. AIntercept waits for each answer before sending the next.
- **Observe:** messages also arrive in order per call.
- **Across calls:** many calls run concurrently, in both modes. Make your
  handlers thread-safe.

### 4.4 `OnCallComplete`

- **When it fires:** once per HTTP call, after the response ended. Always
  asynchronous, in both modes.
- **For a WebSocket:** one per model turn (`ws_turn` 1, 2, …), plus a final one
  when the connection closes (`ws_turn` 0).
- **What it carries:**
  - `request`: the same `RequestInfo` as at start;
  - `response`: `status`, `model` (as served), `input_tokens`,
    `output_tokens`, `cache_read_tokens`, `cache_write_tokens`,
    `reasoning_tokens`, `stop_reason`, `tool_calls` (name and count),
    `ttfb_ms`, `duration_ms`, `response_bytes`, `error`;
  - `verdicts`: what you returned for this call, and whether each was
    `applied`.
- **Denied calls get a record too:** the status they were refused with, and
  `error: "denied by interceptor: …"`.
- **Where the slow work goes:** writing to your warehouse, posting to Slack,
  updating budgets. It can take as long as it needs, up to 5 s per call by
  default.

### 4.5 Failure semantics you must design for

- **Returning a gRPC error, or not answering in time, is a failure.**
  - In enforce, it's handled by `onError`: `deny` (the default) refuses the
    call with a 503, and `allow` lets it through.
  - In observe, it's counted and nothing else happens.
- **Observe delivery is best-effort:**
  - callbacks are queued per observer, delivered in order per call, and never
    retried;
  - if your service is down or backed up, they're dropped and counted.

  So you may see an `OnCallComplete` without its `OnCallStart`, or the
  reverse. Never let a missing callback crash your logic.
- **Calls can repeat:** the same request can be sent twice (client retries).
  Use `call_id` to tell them apart, not the content.

### 4.6 Authenticate the observer

The observer can present a bearer token (`interceptor.tokenSecret`), a client
certificate (`interceptor.tls.clientCert`), or both.

- **Check one of them on every callback**, as the Python example does with the
  `authorization` metadata.
- **With mutual TLS:** configure your gRPC server to require client
  certificates signed by the CA you issue them from.

---

## 5. Test it locally, through a real Envoy

The repo has an end-to-end stack: Envoy with both filters, the observer, the
example Interceptor, and a fixture upstream that echoes requests and says
"silly clown". `make test-interceptor` (Docker required) builds it and runs
nine tests:
- redaction in both directions, for an event stream, a JSON body and a
  WebSocket;
- observe leaving traffic unchanged;
- a deny at call start, with its record;
- recorded verdicts;
- fail-closed with the Interceptor stopped.

**Run the suite against your own Interceptor** by giving it a compose override
that replaces the `redact` service:

```yaml
# my-interceptor.override.yml
services:
  redact:                      # keep this service name: the tests stop and start it
    build: { context: /path/to/my-interceptor }
    image: my-interceptor:dev
    entrypoint: ["/my-interceptor"]   # listening on :9100
```

```bash
INTERCEPTOR_COMPOSE_OVERRIDE=path/to/my-interceptor.override.yml make test-interceptor
```

The tests assert the example's rules: redact "silly clown", refuse
`forbidden-model`. To run them against your service, temporarily add those two
rules. The Python example shows they're five lines. Or use the stack as a
sandbox for your own rules:

```bash
KEEP=1 make test-interceptor      # leaves the stack up afterwards
# Enforced (enforce.test) and observed (observe.test) calls through Envoy on :10000:
curl -s -H 'Host: enforce.test' -H 'content-type: application/json' \
  -d '{"model":"m","messages":[{"role":"user","content":"my secret is hunter2"}]}' \
  http://localhost:10000/v1/messages
# The fixture echoes what it received in the X-Fixture-Received header (base64).
# Every verdict is a row in test/interceptor/out/events.ndjson:
jq -c 'select(.phase=="verdict") | {call: .correlation_id, v: .verdict}' test/interceptor/out/events.ndjson
docker compose --project-directory test/interceptor logs redact    # your service's logs
docker compose --project-directory test/interceptor down -v        # when done
```

The fixture also serves `/v1/json` (a JSON response) and `/ws` (a WebSocket
echo).

**Also unit-test your rules** directly, by calling your handlers with
hand-built `CallStart`/`Message` objects. Cover the edge cases:
- JSON with escaped quotes;
- payloads that aren't JSON;
- empty payloads;
- very large payloads (up to 32 MiB);
- your behaviour with an unexpected `kind`.

---

## 6. Package it

Any container works. The two examples, for reference:

- **Go:** a static binary on distroless. The example is built into AIntercept's
  own image as `/redact-interceptor`. A standalone Dockerfile:

  ```dockerfile
  FROM golang:1.26-bookworm AS build
  WORKDIR /src
  COPY . .
  RUN CGO_ENABLED=0 go build -trimpath -o /out/interceptor ./cmd/interceptor
  FROM gcr.io/distroless/static-debian12:nonroot
  COPY --from=build /out/interceptor /interceptor
  ENTRYPOINT ["/interceptor"]
  ```

- **Python:** `aintercept/examples/python-redact/Dockerfile` generates the stubs
  from the proto at build time:

  ```bash
  docker build -f examples/python-redact/Dockerfile -t my-interceptor aintercept/
  ```

**Requirements for production:**
- **Listen** on a fixed port (the examples use 9100), on all interfaces.
- **Run** as non-root, with a read-only root filesystem.
- **Readiness:** a TCP probe on the gRPC port is enough. If you add the
  standard gRPC health service, use a gRPC probe instead.
- **Handle `SIGTERM` gracefully:** stop accepting, finish in-flight calls, then
  exit. In enforce mode, in-flight calls are users waiting.

---

## 7. Deploy it next to AIntercept

Run it in the same cluster as AIntercept: the observer calls it for every
call, so network latency adds up. The manifests below assume AIntercept's
release is in namespace `aintercept`; adjust the names.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-interceptor
  namespace: aintercept
spec:
  replicas: 3                                  # enforce: ≥3; observe-only: 1 is fine
  selector: { matchLabels: { app: my-interceptor } }
  template:
    metadata: { labels: { app: my-interceptor } }
    spec:
      securityContext: { runAsNonRoot: true }
      topologySpreadConstraints:               # spread replicas across nodes
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector: { matchLabels: { app: my-interceptor } }
      containers:
        - name: interceptor
          image: registry.example.com/my-interceptor:1.0.0
          ports: [{ name: grpc, containerPort: 9100 }]
          env:
            - name: INTERCEPTOR_TOKEN          # the same token the observer sends
              valueFrom: { secretKeyRef: { name: interceptor-token, key: token } }
          readinessProbe: { tcpSocket: { port: 9100 }, periodSeconds: 5 }
          resources:
            requests: { cpu: 200m, memory: 128Mi }
            limits: { memory: 512Mi }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
---
apiVersion: v1
kind: Service
metadata: { name: my-interceptor, namespace: aintercept }
spec:
  selector: { app: my-interceptor }
  ports: [{ name: grpc, port: 9100, targetPort: 9100 }]
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: { name: my-interceptor, namespace: aintercept }
spec:
  maxUnavailable: 1
  selector: { matchLabels: { app: my-interceptor } }
---
# Only the observer may call it (needs a NetworkPolicy-enforcing CNI).
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: my-interceptor-ingress, namespace: aintercept }
spec:
  podSelector: { matchLabels: { app: my-interceptor } }
  policyTypes: ["Ingress"]
  ingress:
    - from: [{ podSelector: { matchLabels: { app: observer } } }]
      ports: [{ port: 9100, protocol: TCP }]
```

**gRPC load balancing:** the observer keeps long-lived HTTP/2 connections, so a
plain ClusterIP Service can pin all of an observer's calls to one of your
replicas. With 2+ observers and 3 replicas this is usually fine. For even
spread, use a headless Service (`clusterIP: None`) and set
`interceptor.endpoint: dns:///my-interceptor.aintercept.svc:9100`. The observer
round-robins across every address the name resolves to. Alternatively, put a
gRPC-aware proxy in front.

**Secrets:**

```bash
# A bearer token the observer sends and your service checks:
kubectl -n aintercept create secret generic interceptor-token \
  --from-literal=token="$(openssl rand -hex 32)"

# TLS: the CA that signed your server's certificate (ca.crt) and, for mutual TLS,
# the observer's client certificate (tls.crt, tls.key):
kubectl -n aintercept create secret generic interceptor-tls \
  --from-file=ca.crt=ca.pem --from-file=tls.crt=observer-client.pem --from-file=tls.key=observer-client-key.pem
```

**Running it outside the cluster:** it works, as long as it's reachable from
the observer pods. Use TLS and a token, and budget the extra round-trip time
against `deadlineMs`.

---

## 8. Wire it in: Helm values

Add to AIntercept's values file. The first deploy is observe only:

```yaml
interceptor:
  endpoint: my-interceptor.aintercept.svc:9100
  content: true            # only if you need OnMessage (section 2)
  deadlineMs: 50           # enforce only; per callback
  onError: deny            # enforce only; deny = fail closed, allow = fail open
  enforce: []              # empty: observe everything, enforce nothing
  tokenSecret: interceptor-token
  tls:
    enabled: false         # true for TLS; then:
    secretName: interceptor-tls
    ca: true               # verify your server with ca.crt (false: system roots)
    clientCert: false      # true: present tls.crt/tls.key (mutual TLS)
    serverName: ""         # the name in your server certificate, if it isn't the endpoint host
```

```bash
helm upgrade aintercept deploy/helm/aintercept -n aintercept -f values.yaml --wait
```

**What the chart does with it:** it sets `INTERCEPTOR_*` on the observer,
mounts the TLS Secret, and, once `enforce` lists anything, adds the enforcing
Envoy filter and routes, an observer PodDisruptionBudget, and two checks:
- enforce needs an endpoint;
- enforce needs at least 2 observer replicas.

Without Helm, set the observer's environment directly. `INTERCEPTOR.md` has the
full list.

**Check it's connected:**

```bash
# The observer logs its configuration at startup:
kubectl -n aintercept logs deploy/observer | grep '"interceptor configured"'
# Delivery counters (queued, delivered, dropped, failed, decided, decide_failed):
kubectl -n aintercept port-forward deploy/observer 9001:9001 &
curl -s localhost:9001/stats | jq .interceptor
# And your service's own logs should show OnCallStart/OnCallComplete for live traffic.
```

`delivered` should climb with traffic. If `failed` climbs instead, your
service is erroring or rejecting the token (section 12).

---

## 9. Run in observe mode and read the verdicts

Run in observe mode for long enough to see a representative week: every
client, every kind of call. Your verdicts are recorded but never applied, so
you can see exactly what enforcing would do.

**Where verdicts are recorded:**
- **Your own logs and metrics.** Log every non-Allow verdict with the
  `call_id`.
- **AIntercept's raw stream.** Every non-Allow verdict, and every failure, is a
  row with `"phase": "verdict"`, keyed by `correlation_id` (your `call_id`):
  `callback`, `action`, `applied`, `mode`, `reason`, `annotations` and
  `latency_ms`. The rows are in the object store under
  `<prefix>/dt=YYYY-MM-DD/hour=HH/instance=<observer>/*.ndjson.gz`. For
  example, with DuckDB:

  ```sql
  SELECT verdict.action, verdict.callback, count(*)
  FROM read_json_auto('s3://<bucket>/<prefix>/dt=2026-11-10/**/*.ndjson.gz')
  WHERE phase = 'verdict' GROUP BY ALL ORDER BY 3 DESC;
  ```

  Join to the call's other rows on `correlation_id`. Its `complete` row has the
  host, path, model, client, identity and tokens.
- **`CallRecord.verdicts`.** Your own `OnCallComplete` receives them.

**What to look for before enforcing:**
- **False positives.** Every deny or replace you'd have applied: would it have
  been right?
- **Latency.** `latency_ms` on verdict rows, and your own timings. p99 must sit
  well under `deadlineMs`. Remember that each streamed event costs one round
  trip when content is on.
- **Errors.** Verdict rows with `action: "error"`, and `/stats`
  `failed`, should be zero.
- **Coverage.** The clients and paths you care about actually show up
  (`Call.client.product`, `Call.path`).

---

## 10. Turn on enforce

**Choose scopes.** An enforce scope is a host, plus optional path prefixes,
from `interception.allowlist`. Start with the narrowest scope that covers what
you need:

| To enforce on | Scope |
|---|---|
| Claude Code (and the Anthropic SDK) model calls | `host: api.anthropic.com`, `paths: ["/v1/messages"]` |
| Codex model turns (over a WebSocket, with an HTTP fallback) | `host: chatgpt.com`, `paths: ["/backend-api/codex/responses"]` |
| ChatGPT in the browser or desktop app: sending a message | `host: chatgpt.com`, `paths: ["/backend-api/f/conversation"]` |
| claude.ai in the browser or desktop app (chats, projects, files) | `host: claude.ai`, `paths: ["/api/organizations/"]` |
| The OpenAI API | `host: api.openai.com`, `paths: ["/v1/chat/completions", "/v1/responses"]` |

Path matching is by prefix. Leave out `paths` to enforce on the whole host.

**Then set the values and upgrade:**

```yaml
interceptor:
  endpoint: my-interceptor.aintercept.svc:9100
  content: true
  deadlineMs: 50
  onError: deny
  enforce:
    - host: api.anthropic.com
      paths: ["/v1/messages"]
observer:
  autoscaling:
    minReplicas: 3          # enforced calls depend on the observer
```

**Check it:**
1. Send a call that your rules should change, through the proxy, and confirm
   the client sees the change. For example, a `curl` with `--resolve` to the
   load balancer, as in `docs/claude-code-test.md`.
2. Confirm verdict rows now say `"applied": true` and `"mode": "enforce"`.
3. Watch `/stats` `decided` (enforced callbacks answered) and `decide_failed`
   (failures, each of which refused or let through a call according to
   `onError`).

**Widen gradually:** add paths and hosts one at a time, each after a period in
observe mode on that scope.

**Roll back:** remove the scope and `helm upgrade`. The routes go back to
observe at once. With an emergency, scaling your Interceptor to zero does
*not* roll back: with `onError: deny`, enforced calls are then refused.
Remove the scope, or set `onError: allow`.

---

## 11. Operating it

**The latency budget, per enforced call:**
- the request is held until its body has fully arrived (it's evaluated whole);
- then `OnCallStart`, then `OnMessage` for the body;
- then, with content on, one `OnMessage` round trip per streamed event before
  that event reaches the user. A long answer can be hundreds of events, so keep
  `OnMessage` in the low single-digit milliseconds.

**What fails, and what users see:**

| Failure | Observe | Enforce |
|---|---|---|
| Your service is down, or answers after `deadlineMs` | Nothing (counted as `failed`) | 503 "interceptor unavailable" with `onError: deny`; passes through with `onError: allow` |
| Your service returns a gRPC error | Nothing | Same as above |
| An observer pod dies | Nothing | Calls in flight on that pod fail; new calls go to the other observers |
| All observers down | Nothing | All enforced calls refused (Envoy fails closed) |

**Size limits:** request and whole-response bodies up to 32 MiB, WebSocket
messages up to 16 MiB. A larger request is refused (413). A larger response,
one that arrives compressed even though AIntercept asked for none, or a
WebSocket on which the server negotiated compression, passes through
unevaluated and is counted.

**Upgrading your service:** do rolling updates, with readiness probes and the
PodDisruptionBudget, and drain on `SIGTERM`. Deploy new rules in observe first
if they're risky. You can run a second instance of your service against
observe-only traffic, by pointing a second AIntercept deployment at it.

**Monitoring:**
- **The observer:** `/stats` `.interceptor`. Alert on `failed` and
  `decide_failed` rising, and on `dropped` in observe.
- **Your service:** latency p99 per callback, and errors.
- **The verdicts:** counts of each action, by scope.

**Changing the proto:** you can't. Only AIntercept changes it, and only by
adding fields in v1. Regenerate when you update the copy you pinned.

---

## 12. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| No callbacks arrive | `interceptor.endpoint` unset or wrong, or a NetworkPolicy blocks the observer | Check the `"interceptor configured"` log line; `kubectl exec` into an observer and try the address; allow ingress from `app: observer` |
| `/stats` `failed` climbs; your logs show nothing | TLS mismatch (server name, CA), or the token is rejected | Check `tls.serverName`, `tls.ca`, the token Secret; try `grpcurl` from inside the cluster |
| Enforced calls get 503 "interceptor unavailable" | Your service missed `deadlineMs` or errored | Check its p99 latency; raise `deadlineMs` only if you understand the cost to users; fix slow I/O in `OnCallStart`/`OnMessage` |
| A replaced response breaks the client | The replacement isn't valid for that message (invalid JSON, or a dropped structural event) | Parse, change, re-serialize; never drop `message_start`, `message_stop`, `response.completed` |
| A phrase in a model's answer isn't caught | It spans two streamed deltas | Keep a per-`call_id` text tail and match across it (§4.3) |
| `helm upgrade` fails: "interceptor.enforce host … isn't in interception.allowlist" | The scope's host isn't intercepted | Add it to `interception.allowlist`, or fix the host name |
| `helm upgrade` fails: "interceptor.enforce needs observer.autoscaling.minReplicas >= 2" | Enforce with one observer | Raise `observer.autoscaling.minReplicas` |
| Verdicts say `applied: false` for a host you meant to enforce | The path isn't in the scope (prefix match) | Check `Call.path` in your logs against the scope's `paths` |
| `OnCallComplete` arrives with no matching `OnCallStart` | Observe delivery is best-effort (a full queue, or a restart) | Expected; make your state tolerant of it (§4.5) |

---

## 13. Checklist

**Before observe:**
- [ ] Generated code from a pinned copy of `interceptor.proto`.
- [ ] `OnCallStart` and `OnMessage` answer fast, with no slow I/O; slow work is in `OnCallComplete` or in the background.
- [ ] Handlers are thread-safe; per-call state is keyed by `call_id` and expires.
- [ ] Replace keeps payloads valid for their kind; Drop never removes structural events.
- [ ] The service checks the token (or mutual TLS).
- [ ] `make test-interceptor` passes against your service (with the example's two rules added), and unit tests cover your own rules.
- [ ] It's deployed with readiness, resources, a NetworkPolicy, and graceful `SIGTERM`.
- [ ] Helm values set `endpoint`, `content` (if needed), the token and TLS.
- [ ] `/stats` shows `delivered` rising, and your logs show live calls.

**Before enforce:**
- [ ] At least a week in observe; false positives reviewed; no `error` verdicts.
- [ ] p99 per callback well under `deadlineMs`.
- [ ] 3+ replicas of your service with a PodDisruptionBudget; `observer.autoscaling.minReplicas` 3+.
- [ ] `onError` chosen deliberately (deny fails closed, allow fails open).
- [ ] The scope is the narrowest one that works; there's a rollback plan (remove the scope).
- [ ] After enabling: a test call shows the change, and verdict rows say `applied: true`.
