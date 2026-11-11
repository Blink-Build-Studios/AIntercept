# AIntercept

**A transparent interception proxy for AI traffic: see how your organization uses AI tools, and act on it.**

AIntercept sits in front of an organization's AI calls: Anthropic, OpenAI, Gemini, the ChatGPT and Codex backend, and claude.ai. It terminates TLS under a trust root you control, forwards each request to the genuine upstream, and tees every request and response to a horizontally scaled Go observer.

- **See it:** a processor turns the observations into tables, and a query API and web UI show who uses which AI tool, how, and at what cost: people, sessions, prompts, the steps inside each prompt, and every call on the wire.
- **Act on it:** the **Interceptor** interface lets your own control plane react to every call. It can record, redact or refuse.

```
client ──TLS──> Envoy (leaf cert minted on the fly) ──verified TLS──> real api.anthropic.com / chatgpt.com / …
                  │  ext_proc: observe (async copy) or enforce (inline, opt-in per host/path)
                  ▼
               observer ──> object store ──> processor ──> Parquet tables ──> query API ──> web UI
                  │  ▲
                  │  certsvc (SDS: mints + renews short-lived leaf certs)
                  ▼
           your Interceptor (gRPC): OnCallStart · OnMessage · OnCallComplete
```

---

## What AIntercept records: two modes

Each deployment chooses (`interceptionMode` in the Helm chart, `INTERCEPTION_MODE` for the observer):

| Mode | Recorded |
|---|---|
| `content-free` (default) | Metadata: provider, host, endpoint, method, status, timings, model, token counts (input, output, cache, reasoning), declared tools and tool calls by name and count (MCP server and tool), the coding agents' own analytics events as they send them, and a salted, irreversible hash of the caller's API key (`sha256(salt ‖ key)`). No request or response body. |
| `full` | All of the above, plus every request and response body: prompt text, commands, patches, responses. Far more data: size, retain and restrict the store accordingly. |

In both modes:
- **Credentials are redacted:** `Authorization`, API keys and cookies are replaced with `<redacted>` before anything is written.
- **Upstream TLS is always verified.** AIntercept re-establishes a verified TLS connection to the genuine provider; it never downgrades or strips transport security.
- **The root CA private key never lives in a container.** certsvc holds only a short-lived *intermediate*, and leaf certs expire in hours and auto-renew.

See [`docs/PRODUCTION.md`](docs/PRODUCTION.md) §4b.

---

## Acting on traffic: the Interceptor

Your control plane implements one gRPC service, [`aintercept/interceptor/v1/interceptor.proto`](aintercept/interceptor/v1/interceptor.proto):

- **`OnCallStart`:** the parsed request (identity, client, model, tools) before it goes upstream. Allow or deny.
- **`OnMessage`:** content, if you enable it: the request body, each streamed event, each WebSocket message. Allow, replace, drop or deny.
- **`OnCallComplete`:** the normalized record of the finished call: tokens, tools used, status, timings, and your verdicts.

The same integration runs in two modes:
- **Observe** (everywhere, by default): callbacks are asynchronous, verdicts are recorded as what would have happened, and traffic never waits.
- **Enforce** (hosts and paths you list): AIntercept waits for your verdict and applies it. Refused calls fail in the provider's own error format, and a slow or unavailable Interceptor fails closed.

Whether the Interceptor sees bodies is its own setting, independent of what AIntercept records: you can redact inline without storing anything. [`docs/INTERCEPTOR.md`](docs/INTERCEPTOR.md) is the reference, and [`docs/BUILDING_AN_INTERCEPTOR.md`](docs/BUILDING_AN_INTERCEPTOR.md) walks through building one and wiring it into a deployment. [`aintercept/examples/redact`](aintercept/examples/redact) is a complete example in about 90 lines: it redacts "silly clown" in both directions and refuses a model.

---

## How it works

A client reaches its ordinary hostname (`api.anthropic.com`), but DNS, scoped to that one host, points it at Envoy on `:443`. Envoy reads the TLS SNI and selects or mints a leaf certificate for that hostname via the Go SDS service (`certsvc`, signed by a CA the client trusts). It terminates TLS, forwards to the genuine upstream over *verified* TLS, and hands the decrypted exchange to the Go `observer` over Envoy's `ext_proc` protocol:

- **Observe mode:** Envoy sends a copy and never waits, so a slow or failed observer can't stall real traffic.
- **Enforce mode:** Envoy forwards only what the observer returns.

The observer writes observations to an S3-compatible store. The **processor** compacts them into Parquet tables, shaped into one common schema across providers with per-provider extensions. The **query API** (Python, FastAPI and DuckDB) serves those tables, and the **UI** (React) reads the API.

See [`docs/architecture.md`](docs/architecture.md) for the design rationale (telemetry mechanism, dynamic certs, DNS isolation, scaling model). [`docs/harness-parity.md`](docs/harness-parity.md) covers what each coding agent sends and how it maps onto the common schema, and [`docs/limitations.md`](docs/limitations.md) is an honest account of what is tested versus assumed.

---

## Quick start (local)

The whole thing on one machine, with your own Claude Code traffic flowing through it: follow [`docs/SETUP.md`](docs/SETUP.md). It's written so a coding agent can follow it step by step.

```bash
make up       # proxy, observer and processor in Docker; the API and UI on this machine
make verify   # one request through the proxy, seen by the observer, processor and API
make down     # stop it (captured data stays in data/, which is gitignored)
```

Then open http://localhost:5173. Routing Claude Code through the proxy (`scripts/route-claude-through-proxy.sh`) needs sudo, so a person runs that step.

The proxy on its own, against a local fixture upstream:

```bash
make run              # generate dev CAs, build images, start the stack, print connection info
make test             # Go unit tests + end-to-end smoke test
make test-interceptor # the Interceptor, observe and enforce, through a real Envoy
make stop             # stop the stack (CA material preserved)
make clean            # DESTRUCTIVE: also delete generated CA keys (prompts first)
```

`make help` lists every target. `make check` runs exactly what CI runs.

---

## Components

| Path | What |
|---|---|
| `envoy/` | Envoy configs (single-host, multi-provider). TLS termination, per-SNI cert selection, `ext_proc`, verified upstreams. |
| `aintercept/` | Go, one binary in three roles: `certsvc` (SDS; mints and auto-renews per-SNI leaf certs), `observer` (`ext_proc` in observe and enforce modes, provider-aware extractors, pluggable sinks, the Interceptor client) and `process` (raw partitions → Parquet tables). Also `schemareport`, which turns unrecognized-shape samples into a structure-only report. |
| `aintercept/interceptor/v1/` | The Interceptor gRPC contract and generated Go code. |
| `aintercept/examples/redact/`, `aintercept/examples/python-redact/` | Example Interceptors, in Go and Python. |
| `api/` | The query API (FastAPI and DuckDB) over the processor's tables. Its Pydantic models are the contract; the UI's types and samples are generated from them (`make regen`). |
| `ui/` | The web UI (React, Vite, Tailwind), with Storybook stories for every component and state. Design in `docs/ui/`. |
| `fixture/` | A local HTTPS origin with synthetic content, for the end-to-end tests. |
| `extractor/` | Offline schema extraction and the sanitizer that makes committable fixtures. |
| `deploy/helm/aintercept/` | Helm chart: Envoy, observer, certsvc, processor and API. One chart, `cloud: aws \| digitalocean`. |
| `deploy/terraform/` | Terraform for AWS (EKS, S3/IRSA) and DigitalOcean (DOKS, Spaces). |
| `deploy/kubernetes/` | Plain manifests (reference). |
| `test/interceptor/` | The Interceptor end-to-end stack (`make test-interceptor`). |
| `scripts/` | Dev CA generation, setup and verification, client-routing helper, smoke tests, repo checks. |

---

## Coding agents and AI apps

Claude Code and Codex are supported end to end. Their sessions, prompts (Codex calls them turns), steps, tool use, rate limits and friction (interruptions, destructive commands, permission prompts) are shown in one common shape, with each provider's extras where it sends them. ChatGPT and claude.ai web traffic is recognized at the conversation level. [`docs/harness-parity.md`](docs/harness-parity.md) tracks exactly what each agent sends and how it is mapped.

The multi-provider Envoy config intercepts, by SNI, the hostnames on an **allowlist**. By default those are:
- `api.anthropic.com`, `claude.ai`, `a.claude.ai`;
- `api.openai.com`, `chatgpt.com`, `ab.chatgpt.com`, `ws.chatgpt.com`;
- `generativelanguage.googleapis.com`.

certsvc **fails closed**: a handshake for a host not on the allowlist gets no cert. Anything that flows through but isn't fully recognized is captured as a **structure-only schema sample**, so coverage is easy to extend. See [`docs/SCHEMA_REPORT.md`](docs/SCHEMA_REPORT.md).

---

## Deploying

- **Kubernetes (Helm):** `deploy/helm/aintercept/` packages Envoy, observer, certsvc, processor and API. It includes:
  - HPA/KEDA autoscaling;
  - zero-downtime rolling drains: Envoy brings a new pod to Ready before draining in-flight streaming turns, for up to 180 s;
  - per-cloud object-store wiring;
  - the Interceptor settings.

  Credentials and the key-hash salt are supplied via Secrets, never `helm --set`. [`docs/PRODUCTION.md`](docs/PRODUCTION.md) covers sizing, the two modes and enforce.
- **DigitalOcean walkthrough:** [`docs/DEPLOY_DOKS.md`](docs/DEPLOY_DOKS.md) has the exact `doctl`, `kubectl` and `helm` steps end to end.
- **Terraform:** `deploy/terraform/{aws,digitalocean}` provision the cluster and the object store.

The object-store defaults in code are placeholders. Set your own bucket, region and endpoint.

---

## Status

This is an early-stage project, developed against live Claude Code and Codex traffic.

- **Tested in CI:**
  - the Go interception and extraction layer (`go test -race`), including enforce-mode unit tests;
  - the API (typed, with a coverage floor);
  - the UI (unit and interaction tests, plus a story per component state).
- **Tested end to end, through a real Envoy:** the Interceptor in observe and enforce (`make test-interceptor`).
- **Run on DigitalOcean:** the Kubernetes/Helm packaging and the Terraform.

See [`docs/limitations.md`](docs/limitations.md) for the honest tested-versus-assumed breakdown.

---

## License

[Apache License 2.0](LICENSE). See [`NOTICE`](NOTICE).
