# AIntercept

**A transparent interception proxy for AI API traffic that captures structure and metadata, never content.**

AIntercept sits in front of an organization's AI API calls (Anthropic, OpenAI, Gemini, and the ChatGPT/Codex backend). It terminates TLS under a trust root you control, forwards each request **unchanged** to the genuine upstream, and tees a **content-free** observation of every request and response to a horizontally scaled Go observer. A processor turns those observations into tables, and a query API and web UI show who uses which coding agent, how, and at what cost: people, sessions, prompts, the steps inside each prompt, and every call on the wire.

It's built so that it **cannot** silently become a surveillance tool. By design it records the *shape* of AI usage (who called which model, with which tools, how many tokens, how long it took), not prompts or responses.

```
client ──TLS──> Envoy (leaf cert minted on the fly) ──verified TLS──> real api.anthropic.com / chatgpt.com / …
                  │  tee (decrypted, async, non-blocking)
                  ▼
               observer ──> object store (body-stripped NDJSON)
                  ▲                │
               certsvc             ▼
       (SDS: mints + renews    processor ──> Parquet tables ──> query API ──> web UI
        short-lived leaf certs)
```

---

## The content-free contract

This is the point of the project, so it comes first.

- **What is captured:** provider, host, endpoint, HTTP method and status, model, input/output/cache/reasoning token counts, timings, declared tools and the names and counts of tool calls (MCP server and tool names), stream flag, and a **salted, irreversible** hash of the caller's API key (`sha256(salt ‖ key)`, never the key itself). Coding agents' own analytics events are kept as the client sends them. Claude Code's include each shell command's program name and exit code.
- **What is never captured:** prompt text, completion text, system prompts, tool arguments, tool results, file contents, or any other request or response body content.
- **Secrets are redacted** before anything is logged. `Authorization`, API keys and cookies are replaced with `<redacted>` on every path.
- **Upstream TLS is always verified.** AIntercept is not a way to downgrade or strip transport security. It re-establishes a verified TLS connection to the genuine provider.
- **The root CA private key never lives in a container.** certsvc holds only a short-lived *intermediate*; leaf certs expire in hours and auto-renew.

### Development-only body capture

For building and debugging extractors there is a full-interception mode that records bodies: `DEV_LOG_BODIES`, `DEV_CAPTURE_FULL_BODIES`, and the Helm chart's `fullInterception` value. It is fenced off:

- The code only exists in images built with `-tags devcapture`. Production builds compile it out.
- The chart refuses `fullInterception` while its admission policy (on by default) is enabled.
- The UI says which mode a deployment runs in (About this data → What is kept).

**Never enable it for anyone else's traffic, or against a shared or durable store.** Doing so breaks the contract above. See [`docs/PRODUCTION.md`](docs/PRODUCTION.md) and [`docs/limitations.md`](docs/limitations.md).

---

## How it works

A client reaches its ordinary hostname (`api.anthropic.com`), but DNS, scoped to that one host, points it at Envoy on `:443`. Envoy reads the TLS SNI and selects or mints a leaf certificate for that hostname via the Go SDS service (`certsvc`, signed by a CA the client trusts). It terminates TLS, forwards to the genuine upstream over *verified* TLS, and tees the decrypted HTTP exchange to the Go `observer` over Envoy's `ext_proc` protocol. The observer acknowledges Envoy immediately and does all extraction on a separate goroutine, so a slow or failed observer can never stall real traffic.

The observer writes body-stripped observations to an S3-compatible store. The **processor** compacts them into Parquet tables, shaped into one common schema across providers, with per-provider extensions. The **query API** (Python, FastAPI and DuckDB) serves those tables, and the **UI** (React) reads the API.

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
make run           # generate dev CAs, build images, start the stack, print connection info
make test          # Go unit tests + end-to-end smoke test
make observer-logs # watch content-free JSON telemetry events stream by
make stop          # stop the stack (CA material preserved)
make clean         # DESTRUCTIVE: also delete generated CA keys (prompts first)
```

`make help` lists every target. `make check` runs exactly what CI runs.

---

## Components

| Path | What |
|---|---|
| `envoy/` | Envoy configs (single-host, multi-provider). TLS termination, per-SNI cert selection, `ext_proc` tee, verified upstreams. |
| `aintercept/` | Go, one binary in three roles: `aintercept certsvc` (SDS; mints and auto-renews per-SNI leaf certs), `aintercept observer` (`ext_proc` tee, buffer-and-ack, provider-aware extractors, pluggable sinks: stdout, file, S3-compatible) and `aintercept process` (raw partitions → Parquet tables). Also `schemareport`, which turns unrecognized-shape samples into a structure-only report (key names and value *types*, never values). |
| `api/` | The query API (FastAPI and DuckDB) over the processor's tables. Its Pydantic models are the contract; the UI's types and samples are generated from them (`make regen`). |
| `ui/` | The web UI (React, Vite, Tailwind), with Storybook stories for every component and state. Design in `docs/ui/`. |
| `fixture/` | A local HTTPS origin with synthetic content, for the end-to-end smoke test. |
| `extractor/` | Offline schema extraction and the sanitizer that makes committable fixtures. |
| `deploy/helm/aintercept/` | Helm chart: Envoy, observer, certsvc, processor and API. One chart, `cloud: aws \| digitalocean`. |
| `deploy/terraform/` | Terraform for AWS (EKS, S3/IRSA) and DigitalOcean (DOKS, Spaces). |
| `deploy/kubernetes/` | Plain manifests (reference). |
| `scripts/` | Dev CA generation, setup and verification, client-routing helper, smoke tests, repo checks. |

---

## Coding agents

Claude Code and Codex are supported end to end. Their sessions, prompts (Codex calls them turns), steps, tool use, rate limits and friction (interruptions, destructive commands, permission prompts) are shown in one common shape, with each provider's extras where it sends them. ChatGPT and claude.ai web traffic is recognized at the conversation level. [`docs/harness-parity.md`](docs/harness-parity.md) tracks exactly what each agent sends and how it is mapped.

## Multi-provider

The multi-provider Envoy config intercepts, by SNI, the hostnames on an **allowlist**. By default those are `api.anthropic.com`, `api.openai.com`, `generativelanguage.googleapis.com` and `chatgpt.com` (the ChatGPT/Codex backend). certsvc **fails closed**: a handshake for a host not on the allowlist gets no cert. The extractors are provider-aware; Gemini, for example, carries the model in the URL path and usage in `usageMetadata`, and Anthropic in the body. Anything that flows through but isn't fully recognized is captured as a **structure-only schema sample**, so coverage is easy to extend. See [`docs/SCHEMA_REPORT.md`](docs/SCHEMA_REPORT.md).

---

## Deploying

- **Kubernetes (Helm):** `deploy/helm/aintercept/` packages Envoy, observer, certsvc, processor and API. It includes:
  - HPA/KEDA autoscaling;
  - zero-downtime rolling drains: Envoy brings a new pod to Ready before draining in-flight streaming turns, for up to 180 s;
  - per-cloud object-store wiring.

  Credentials and the key-hash salt are supplied via Secrets, never `helm --set`. [`docs/PRODUCTION.md`](docs/PRODUCTION.md) covers sizing and hardening.
- **DigitalOcean walkthrough:** [`docs/DEPLOY_DOKS.md`](docs/DEPLOY_DOKS.md) has the exact `doctl`, `kubectl` and `helm` steps end to end.
- **Terraform:** `deploy/terraform/{aws,digitalocean}` provision the cluster and the object store.

The object-store defaults in code (`your-telemetry-bucket` and the like) are placeholders. Set your own bucket, region and endpoint.

---

## Status

This is an early-stage project, developed against live Claude Code and Codex traffic.

- **Tested in CI:** the Go interception and extraction layer (`go test -race`), the API (typed, with a coverage floor), and the UI (unit and interaction tests, plus a story per component state).
- **Run on DigitalOcean:** the Kubernetes/Helm packaging and the Terraform.

See [`docs/limitations.md`](docs/limitations.md) for the honest tested-versus-assumed breakdown.

---

## License

[Apache License 2.0](LICENSE). See [`NOTICE`](NOTICE).
