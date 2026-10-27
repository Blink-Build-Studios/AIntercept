# AIntercept

**A transparent interception proxy for AI API traffic that captures structure and metadata — never content.**

AIntercept sits in front of your organization's AI API calls (Anthropic, OpenAI,
Gemini, and the ChatGPT/Codex backend), terminates TLS under a trust root you
control, forwards each request **unchanged** to the authentic upstream, and tees
a **content-free** observation of every request/response to a horizontally-scaled
Go observer. The observer extracts model, token counts, tool/MCP usage, status,
and a salted key-hash identity — and writes them to object storage as gzipped
NDJSON.

It is built so that it **cannot** silently become a surveillance tool: by
design it records the *shape* of AI usage (who called which model, with which
tools, how many tokens, what it cost), not prompts or responses.

```
client ──TLS──> Envoy (leaf cert minted on the fly) ──verified TLS──> real api.anthropic.com / api.openai.com / …
                  │  tee (decrypted, async, non-blocking)
                  ▼
               observer ──> object store (telemetry NDJSON + schema-samples/)
                  ▲
               certsvc (SDS: mints + auto-renews short-lived leaf certs)
```

---

## The content-free contract

This is the point of the project, so it comes first.

- **What is captured:** provider, host, endpoint, HTTP method + status, model,
  input/output/cache token counts, declared tools and observed tool calls (MCP
  server + tool names), stream flag, and a **salted, irreversible** hash of the
  caller's API key (`sha256(salt ‖ key)`, never the key itself).
- **What is never captured:** prompt text, completion text, system prompts,
  tool arguments, tool results, or any request/response body content.
- **Secrets are redacted** before anything is logged — `Authorization`, API
  keys, and cookies are replaced with `<redacted>` on every path.
- **Upstream TLS is always verified.** AIntercept is not a way to downgrade or
  strip transport security; it re-establishes a verified TLS connection to the
  genuine provider.
- **The root CA private key never lives in a container.** certsvc holds only a
  short-lived *intermediate*; leaf certs expire in hours and auto-renew.

There are developer-only flags (`DEV_LOG_BODIES`, `DEV_CAPTURE_FULL_BODIES`) that
make decrypted bodies visible for local debugging against an ephemeral sink.
**Do not enable them against any shared or durable store** — doing so breaks the
contract above. See [`docs/limitations.md`](docs/limitations.md).

---

## How it works

A client reaches its ordinary hostname (`api.anthropic.com`), but DNS — scoped to
that one host — points it at Envoy on `:443`. Envoy reads the TLS SNI, selects or
mints a leaf certificate for that hostname via the Go SDS service (`certsvc`,
signed by a CA the client trusts), terminates TLS, forwards to the authentic
upstream over *verified* TLS, and tees the decrypted HTTP exchange to the Go
`observer` over Envoy's `ext_proc` protocol. The observer acknowledges Envoy
immediately and does all extraction on a separate goroutine, so a slow or failed
observer can never stall real traffic.

See [`docs/architecture.md`](docs/architecture.md) for the full design rationale
(telemetry mechanism, dynamic certs, DNS isolation, scaling model) and
[`docs/limitations.md`](docs/limitations.md) for an honest accounting of what is
tested versus assumed.

---

## Quick start (local)

Requires Docker, `make`, and Go.

```bash
make run           # generate dev CAs, build images, start the stack, print connection info
make test          # Go unit tests + end-to-end smoke test
make info          # reprint DNS + trust config for a test client
make observer-logs # watch content-free JSON telemetry events stream by
make stop          # stop the stack (CA material preserved)
make clean         # DESTRUCTIVE: also delete generated CA keys (prompts first)
```

`make run` prints a host-reachable IP + TCP `:443`, the intercepted hostname, the
dev root CA path, and the DNS override to configure. Point a client at it:

```bash
curl --cacert certs/root-ca.crt \
     --resolve api.anthropic.com:443:127.0.0.1 \
     https://api.anthropic.com/v1/messages -X POST -d '{}' -i | head
```

A `401` from the upstream (rather than a TLS error) means interception **and**
verified upstream forwarding both work. The telemetry for that call shows up in
`make observer-logs` with no body content in it.

### Prove the content-free property to yourself

```bash
DEV_LOG_BODIES=true make run      # dev-only: makes decrypted bodies visible locally
# send a request carrying a synthetic marker; watch it appear in body_preview —
# while Authorization / api-key / cookie still render as <redacted>.
```

That flag exists precisely so you can *verify* redaction. It is off by default and
belongs only on a throwaway local sink.

---

## Components

| Path | What |
|---|---|
| `envoy/` | Envoy configs (base single-host, multi-provider). TLS termination, per-SNI cert selection, `ext_proc` tee, verified upstreams. |
| `certsvc/` | Go SDS cert service — mints per-SNI leaf certs on demand and auto-renews them before expiry (hot-swap over SDS, no restarts). |
| `observer/` | Go `ext_proc` observer — non-blocking buffer-and-ack, provider-aware extractors, pluggable sinks (stdout, file, S3-compatible). |
| `observer/cmd/schemareport/` | CLI that turns unrecognized-shape samples into a **redacted** structure-only report (key names + value *types*, never values). |
| `fixture/` | A local HTTPS origin with synthetic content, for the end-to-end smoke test. |
| `extractor/`, `etl/` | Offline extraction + rollup (NDJSON → parquet/DuckDB) and model-price cost attribution. |
| `deploy/helm/aintercept/` | Helm chart. One chart, `cloud: aws \| digitalocean`. |
| `deploy/terraform/` | Terraform for AWS (EKS + S3/IRSA) and DigitalOcean (DOKS + Spaces). |
| `deploy/kubernetes/` | Plain manifests (reference). |
| `scripts/` | Dev CA generation, connection info, client-routing helper, smoke tests. |

---

## Multi-provider

The multi-provider Envoy config intercepts, by SNI, the hostnames on an
**allowlist** — `api.anthropic.com`, `api.openai.com`,
`generativelanguage.googleapis.com`, and `chatgpt.com` (the ChatGPT/Codex
backend) by default. certsvc **fails closed**: a handshake for a host not on the
allowlist gets no cert. The observer's extractors are provider-aware (e.g. Gemini
carries the model in the URL path and usage in `usageMetadata`; Anthropic in the
body). Anything that flows through but isn't fully recognized is captured as a
**structure-only schema sample** so coverage is easy to extend — see
[`docs/SCHEMA_REPORT.md`](docs/SCHEMA_REPORT.md).

---

## Deploying

- **Kubernetes (Helm):** `deploy/helm/aintercept/` packages Envoy + observer +
  certsvc with HPA/KEDA autoscaling, zero-downtime rolling drains (Envoy surges a
  new pod to Ready before draining in-flight streaming turns for up to 180s), and
  per-cloud object-store wiring. Credentials and the key-hash salt are supplied
  via Secrets, never `helm --set`.
- **DigitalOcean walkthrough:** [`docs/DEPLOY_DOKS.md`](docs/DEPLOY_DOKS.md) — the
  exact `doctl`/`kubectl`/`helm` steps end-to-end.
- **Terraform:** `deploy/terraform/{aws,digitalocean}` provision the cluster +
  object store.

The object store defaults in code (`your-telemetry-bucket`, etc.) are
placeholders — set your own bucket, region, and endpoint.

---

## Status

This is an early-stage project. The local interception path and the Go
extraction/telemetry layer are tested (`make test` + unit tests across the
`observer` and `certsvc` modules). The Kubernetes/Helm packaging and multi-cloud
Terraform have been run against DigitalOcean. See
[`docs/limitations.md`](docs/limitations.md) for the honest tested-vs-assumed
breakdown.

---

## License

[Apache License 2.0](LICENSE). See [`NOTICE`](NOTICE).
