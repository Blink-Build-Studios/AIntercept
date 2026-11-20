# Building an Interceptor, and wiring it into a deployment

This guide takes you from an empty directory to an Interceptor that is running
next to AIntercept, observing every AI call, and then enforcing a policy on
the calls you choose. It assumes you know gRPC and Kubernetes. It doesn't
assume anything about AIntercept beyond what's here.

[`INTERCEPTOR.md`](INTERCEPTOR.md) is the reference: the protocol, the modes
and the settings. [`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md)
shows how to write the code: in Go with package `intercept`, every kind of
answer worked through, and the raw protocol. This document is the walkthrough
from code to production.

**Contents**
1. [What you're building](#1-what-youre-building)
2. [Decide what your Interceptor needs](#2-decide-what-your-interceptor-needs)
3. [Get the contract and write the code](#3-get-the-contract-and-write-the-code)
4. [What every Interceptor must handle](#4-what-every-interceptor-must-handle)
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
                  │                                      OnCallStart(CallStart)         → CallVerdict
                  ▼                                      OnRequest(RequestMessages)     → RequestVerdict
               object store, processor, API, UI          OnReply(ResponseMessage)       → MessageVerdict
                                                         OnToolCall(ResponseMessage)    → MessageVerdict
                                                         OnCallComplete(CallRecord)     → Ack
```

You write a **gRPC server**. AIntercept's observer is the **client**: it dials
your service and calls it for every intercepted call. You never talk to Envoy,
and you never handle TLS interception, certificates or provider protocols.
AIntercept parses all of that and hands you each call's new messages, in one
shape for every provider.

| | What you get | What you return |
|---|---|---|
| `OnCallStart` | Who's calling (key hash, IP, verified email and account, device, session), which client (Claude Code, Codex, …), which model, effort, declared tools | Accept, or Respond |
| `OnRequest` | The call's new messages, before they go upstream: the prompt, tool results, the harness's notes. **Only if content is enabled.** | Accept or Replace per message; Respond; or Fetch a part sent by reference |
| `OnReply` | The model's reply text, piece by piece as it streams, before the client sees it. **Only with content.** | Accept, Replace, Hold or Respond, and `keep` |
| `OnToolCall` | Each tool call the model makes, whole, before the agent sees it. **Only with content.** | Accept, Replace (new arguments) or Respond |
| `OnCallComplete` | The finished call: status, tokens, tools used, timings, error, and the answers you gave | Ack |

Respond answers for the model: the client gets your text as the model's reply.
There's no Deny: rewrite, or answer, rather than refuse. Leave `OnReply` or
`OnToolCall` unimplemented and AIntercept doesn't hold replies for you.

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
| Alert on or count by model, client, person/key, host or path | No | `OnCallStart`, `OnCallComplete` |
| Answer certain calls yourself (a model nobody may use) | No | `OnCallStart` (Respond) |
| Track usage (calls, tokens) in real time | No | `OnCallComplete` |
| Redact secrets or PII in prompts or tool results | **Yes** | `OnRequest` (Replace) |
| Redact secrets or PII in the model's replies | **Yes** | `OnReply` (Replace, `keep`) |
| Rewrite, or answer, prompts that match a pattern or classifier | **Yes** | `OnRequest` (Replace, Respond) |
| Rewrite or stop tool calls the model makes, before the agent runs them | **Yes** | `OnToolCall` (Replace, Respond) |

Content is a deployment setting (`interceptor.content`). It's independent of
whether AIntercept *records* bodies (`interceptionMode`). With content on, your
service receives every prompt and reply, so treat it as data that is as
sensitive as the traffic itself.

**Does it need to enforce, or only observe?**

- **Only observe** (alerting, audit, usage): latency doesn't matter. Your
  service can be a single replica, can call slow systems (Slack, a SIEM, a
  database), and can restart freely.
- **Enforce** (redacting, rewriting, answering): `OnCallStart`, `OnRequest`,
  `OnReply` and `OnToolCall` are in the call's path, with a deadline of 50 ms
  per callback by default. A slow or unavailable service means refused calls.
  You'll need multiple replicas, low latency, and no slow I/O inside those
  handlers.

Whatever you decide, **build it as if it will enforce**: answer fast, and keep
the slow work in `OnCallComplete` or in the background. Then turning enforce on
later is only a configuration change.

---

## 3. Get the contract and write the code

The contract is one file: `aintercept/interceptor/v2/interceptor.proto`
(package `aintercept.interceptor.v2`). Pin the version you build against, and
handle enum values you don't recognize as "unspecified". AIntercept's release
notes call out any breaking change to it.

### Go

Use package `intercept`: rules for the common case, hooks for full control,
and watches for phrases in streamed text, with the gRPC server, receive
limits, health checks and graceful shutdown done for you.
[`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md) is its guide.

```go
import "github.com/Blink-Build-Studios/aintercept/aintercept/intercept"

func main() {
	log.Fatal(intercept.Run(
		intercept.OnMessage(intercept.Contains("silly clown")).Replace("[redacted]"),
	))
}
```

The generated code is importable directly too, for the raw protocol:
`pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"`.

### Python

```bash
pip install grpcio==1.71.0 grpcio-tools==1.71.0 protobuf
python -m grpc_tools.protoc -I proto --python_out=. --grpc_python_out=. proto/interceptor/v2/interceptor.proto
touch interceptor/__init__.py interceptor/v2/__init__.py
```

That gives you `interceptor/v2/interceptor_pb2.py` (messages) and
`interceptor_pb2_grpc.py` (the servicer base class).
`aintercept/examples/python-redact/server.py` is a complete one on the raw
protocol.

### Node / TypeScript

Either load the proto at runtime with `@grpc/proto-loader` and `@grpc/grpc-js`,
or generate types with `grpc-tools` or `ts-proto`. The service name is
`aintercept.interceptor.v2.Interceptor`.

### Anything else

Any language with gRPC support works. Generate a server for
`aintercept.interceptor.v2.Interceptor` with that language's `protoc` plugin.

---

## 4. What every Interceptor must handle

Three complete examples live in the repo:

- `aintercept/examples/rabbit` (Go, package `intercept`): two rules that
  answer two phrases in the user's prompts for the model.
- `aintercept/examples/redact` (Go, package `intercept`): redacts "silly
  clown" in every message both ways, answers the model `forbidden-model` with
  a refusal, and logs who makes each call. It passes the end-to-end suite in
  section 5.
- `aintercept/examples/python-redact/server.py` (Python, the raw protocol):
  redact's behaviour for prompts, tool results and reply text, and the same
  suite.

What each callback carries and how each answer behaves is in
[`INTERCEPTOR.md`](INTERCEPTOR.md) ("Callbacks", "Answers", "Reply text").
The rest of this section is what you need to know whatever you write.

### 4.1 Every callback carries `Call`

| Field | Use it for |
|---|---|
| `call_id` | The key for any per-call state you keep. It's the same on every callback for the call, and is AIntercept's `correlation_id`. |
| `mode` | `MODE_OBSERVE` or `MODE_ENFORCE`: whether your answer will be applied. Usually you answer the same either way; the mode is for your own logs and metrics. |
| `transport` | `HTTP` or `WEBSOCKET`. |
| `host`, `method`, `path` | The request as the client sent it, including the query string. |
| `identity` | `key_hash` (a salted hash of the API key or token; stable per credential, never reversible) and `key_hint` (e.g. `sk-ant…a3f9`), plus `ip`, `org_id`, `device_id`, `session_id`, `email`, `name` and `email_source`, `reported_account_id` (what the client says), `verified_account_id` (what the provider confirmed for the credential) and `account_mismatch`. See `docs/INTERCEPTOR.md`, Identity. |
| `client` | `product` (`claude_code`, `codex`, or empty), `user_agent`, and the client's identifying headers (`originator`, `x-app`, `anthropic-version`, …). |

To map identities to people or teams, keep your own table keyed on
`key_hash`, `verified_account_id` or `email`, and look it up in memory. Grant
privileges only when `email_source` is `IDENTITY_SOURCE_CREDENTIAL`: the
provider confirmed that person for the call's credential. The other sources are
what the client says.

### 4.2 Failure semantics you must design for

- **Returning a gRPC error, an answer that can't be applied, or not answering
  in time, is a failure.**
  - In enforce, it's handled by `onError`: `deny` (the default) refuses the
    call with a 503 in the provider's own error format (or, once the reply
    has started, ends it with the provider's error event), and `allow` lets it
    through.
  - In observe, it's counted and nothing else happens.
- **Observe delivery is best-effort:**
  - callbacks are queued per observer, delivered in order per call, and never
    retried;
  - if your service is down or backed up, they're dropped and counted.

  So you may see an `OnCallComplete` without its `OnCallStart`, or the
  reverse. Never let a missing callback crash your logic.
- **Calls can repeat:** the same request can be sent twice (client retries).
  Use `call_id` to tell them apart, and message ids (stable within a session)
  to recognize a message you've seen.
- **Order and concurrency:** within one call, callbacks arrive one at a time,
  in order, in both modes. Many calls run concurrently: make your handlers
  thread-safe.

### 4.3 Authenticate the observer

The observer can present a bearer token, either a static one
(`interceptor.tokenSecret`) or its own rotating Kubernetes service-account
token (`interceptor.serviceAccountToken`, a JWT you verify against the
cluster's OIDC issuer: docs/INTERCEPTOR.md, Authentication), a client
certificate (`interceptor.tls.clientCert`), or a token and a certificate.

- **Check one of them on every callback**, as the Python example does with the
  `authorization` metadata.
- **With mutual TLS:** configure your gRPC server to require client
  certificates signed by the CA you issue them from.

---

## 5. Test it locally, through a real Envoy

The repo has an end-to-end stack: Envoy with both filters, the observer, the
example redact Interceptor, and a fixture upstream that echoes requests and
replies in the provider's format. `make test-interceptor` (Docker required)
builds it and tests, against redact:
- a prompt redacted on the way out, and the reply on the way back, even with
  the phrase split across streamed pieces;
- observe leaving traffic unchanged;
- `forbidden-model` answered with the refusal at call start, never reaching
  the provider, and its record;
- recorded verdicts;
- fail-closed with the Interceptor stopped;

and, against the suite's own Interceptor, every kind of answer in every
format (Anthropic Messages, Responses over HTTP and Codex's WebSocket, Chat
Completions).

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

The tests assert the example's rules: redact "silly clown" both ways, and
answer `forbidden-model` with "forbidden-model isn't approved for use here.".
To run them against your service, temporarily add those rules; the Python
example shows them on the raw protocol. Or use the stack as a sandbox for your
own rules:

```bash
KEEP=1 make test-interceptor      # leaves the stack up afterwards
# A Claude Code call through Envoy on :10000, to your Interceptor (x-e2e-observer: main),
# enforced (add -H 'x-e2e-mode: observe' to observe it):
curl -s -H 'Host: api.anthropic.com' -H 'x-e2e-observer: main' -H 'content-type: application/json' \
  -d '{"model":"m","stream":true,"messages":[{"role":"user","content":"my secret is hunter2"}]}' \
  http://localhost:10000/v1/messages
# The fixture echoes what it received in the X-Fixture-Received header (base64), and
# replies with "fixture_text" (a list of pieces) when the request has it.
# Every verdict is a row in test/interceptor/out/events.ndjson:
jq -c 'select(.phase=="verdict") | {call: .correlation_id, v: .verdict}' test/interceptor/out/events.ndjson
docker compose --project-directory test/interceptor logs redact    # your service's logs
docker compose --project-directory test/interceptor down -v        # when done
```

**Also unit-test your rules.** In Go, `intercepttest` runs them exactly as
the observer would and reports what the client sees
([`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md), "Test
it"; `examples/redact/main_test.go` is a complete one). On the raw protocol,
call your handlers with hand-built messages. Cover the edge cases:
- a phrase split across reply pieces;
- a part sent by reference (`ref`, with only a preview);
- media and opaque parts, which come back unchanged;
- the harness's notes (`ROLE_SYSTEM`) beside the user's prompt;
- unknown enum values.

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
    - from: [{ podSelector: { matchLabels: { app: envoy } } }]   # the observer runs in each envoy pod
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
  content: true            # only if you need the messages (section 2)
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
kubectl -n aintercept logs deploy/envoy -c observer | grep '"interceptor configured"'
# Delivery counters (queued, delivered, dropped, failed, decided, decide_failed):
kubectl -n aintercept port-forward deploy/envoy 9001:9001 &
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
- **The observer's event stream.** Every non-Allow verdict, and every failure,
  is a row with `"phase": "verdict"`, keyed by `correlation_id` (your
  `call_id`): `callback`, `action`, `applied`, `mode`, `reason`, `annotations`
  and `latency_ms`. The rows are in the observer's event output: its stdout
  (one JSON row per line) and, when `OBSERVER_CAPTURE_FILE` is set, that file.
  Join to the call's other rows on `correlation_id`. Its `complete` row has the
  host, path, model, client, identity and tokens. The call itself, with its
  rows, is also in the raw files in the object store (`<prefix>/raw/`, gzip
  NDJSON: lines starting `{"record":` are records, the rest are rows).
- **`CallRecord.verdicts`.** Your own `OnCallComplete` receives them.

**What to look for before enforcing:**
- **False positives.** Every replace or respond you'd have applied: would it
  have been right?
- **Latency.** `latency_ms` on verdict rows, and your own timings. p99 must sit
  well under `deadlineMs`. Remember that with content on, each piece of reply
  text and each tool call costs one round trip.
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

Path matching is by prefix. Leave out `paths` to enforce on the whole host. ChatGPT and claude.ai on the web aren't read as
conversations yet: enforced, their calls get `OnCallStart` and
`OnCallComplete`, and their content goes as it came.

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
envoy:
  autoscaling:
    minReplicas: 3          # enforced calls depend on the observer, which runs in each envoy pod
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
- the request is held until its body has fully arrived (it's read whole);
- then `OnCallStart`, then, with content on, `OnRequest` for its new messages;
- then, with content on and an `OnReply`, one round trip per piece of reply
  text before it reaches the user (pieces that arrive meanwhile gather into
  the next call), and with an `OnToolCall`, one per tool call. Keep those
  handlers in the low single-digit milliseconds.

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
unevaluated and is counted. A request AIntercept can't read as a conversation
goes upstream untouched, without `OnRequest`, and is counted by reason
(`/stats`: `unparsed_reasons`).

**Upgrading your service:** do rolling updates, with readiness probes and the
PodDisruptionBudget, and drain on `SIGTERM`. Deploy new rules in observe first
if they're risky. You can run a second instance of your service against
observe-only traffic, by pointing a second AIntercept deployment at it.

**Monitoring:**
- **The observer:** `/stats` `.interceptor`. Alert on `failed` and
  `decide_failed` rising, and on `dropped` in observe.
- **Your service:** latency p99 per callback, and errors.
- **The verdicts:** counts of each action, by scope.

**Changing the proto:** you can't. Only AIntercept changes it, and its
release notes call out anything that breaks. Regenerate when you update the
copy you pinned.

---

## 12. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| No callbacks arrive | `interceptor.endpoint` unset or wrong, or a NetworkPolicy blocks the observer | Check the `"interceptor configured"` log line; `kubectl exec` into an observer and try the address; allow ingress from `app: envoy` (the observer runs in each envoy pod) |
| `/stats` `failed` climbs; your logs show nothing | TLS mismatch (server name, CA), or the token is rejected | Check `tls.serverName`, `tls.ca`, the token Secret; try `grpcurl` from inside the cluster |
| Enforced calls get 503 "interceptor unavailable" | Your service missed `deadlineMs`, errored, or gave an answer that can't be applied | Check its p99 latency and the verdict rows' `reason`; raise `deadlineMs` only if you understand the cost to users; fix slow I/O in the handlers |
| Verdict rows say `error` with "invalid answer" | A Replace that doesn't return every part by id, in order, or changes media or an opaque part; a Respond with no text; a second Fetch | Return the message as you got it with only its text changed (`INTERCEPTOR.md`, "Answers") |
| A phrase in a model's answer isn't caught | It spans two streamed pieces, and each piece was accepted whole | In Go, use a watch (a `Contains` or `Matches` rule on the reply is one); on the raw protocol, answer with `keep` to hold back a tail that could still become it |
| No `OnRequest` for some calls | The request isn't a conversation AIntercept reads (another endpoint, a token count), or adds no new message | Expected; `/stats` counts them (`unparsed_reasons`, `no_new_messages`) |
| `helm upgrade` fails: "interceptor.enforce host … isn't in interception.allowlist" | The scope's host isn't intercepted | Add it to `interception.allowlist`, or fix the host name |
| `helm upgrade` fails: "interceptor.enforce needs envoy.autoscaling.minReplicas >= 2" | Enforce with one ingest pod | Raise `envoy.autoscaling.minReplicas` |
| Verdicts say `applied: false` for a host you meant to enforce | The path isn't in the scope (prefix match) | Check `Call.path` in your logs against the scope's `paths` |
| `OnCallComplete` arrives with no matching `OnCallStart` | Observe delivery is best-effort (a full queue, or a restart) | Expected; make your state tolerant of it (§4.2) |

---

## 13. Checklist

**Before observe:**
- [ ] Built against a pinned copy of `interceptor.proto` (or a pinned AIntercept version, for package `intercept`).
- [ ] Every callback but `OnCallComplete` answers fast, with no slow I/O; slow work is in `OnCallComplete` or in the background.
- [ ] Handlers are thread-safe; per-call state is keyed by `call_id` and expires.
- [ ] A Replace returns the message as it came, every part by id and in order, with only its text changed.
- [ ] The service checks the token (or mutual TLS).
- [ ] `make test-interceptor` passes against your service (with the example's rules added), and unit tests cover your own rules.
- [ ] It's deployed with readiness, resources, a NetworkPolicy, and graceful `SIGTERM`.
- [ ] Helm values set `endpoint`, `content` (if needed), the token and TLS.
- [ ] `/stats` shows `delivered` rising, and your logs show live calls.

**Before enforce:**
- [ ] At least a week in observe; false positives reviewed; no `error` verdicts.
- [ ] p99 per callback well under `deadlineMs`.
- [ ] 3+ replicas of your service with a PodDisruptionBudget; `envoy.autoscaling.minReplicas` 3+.
- [ ] `onError` chosen deliberately (deny fails closed, allow fails open).
- [ ] The scope is the narrowest one that works; there's a rollback plan (remove the scope).
- [ ] After enabling: a test call shows the change, and verdict rows say `applied: true`.
