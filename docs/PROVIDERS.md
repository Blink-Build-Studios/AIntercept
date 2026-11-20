# Onboarding an inference provider

Support for a provider is spread over Envoy configs, Go packages, SQL in two
languages and several docs. This is the list of every place a new provider (or
a new client of an existing one) has to be added, with the file, what to add
and how to test it. Work down it in order; the last section says when the
provider is done.

The examples are the providers supported today: Anthropic (`api.anthropic.com`,
Claude Code), OpenAI (`api.openai.com`), the ChatGPT backend (`chatgpt.com`,
Codex), Gemini (`generativelanguage.googleapis.com`) and claude.ai.

## The test rule

**Every transformation, extraction and callback for a provider has a unit test
on an anonymized real capture: one test per extracted field per traffic
shape.** A traffic shape is one way the provider's traffic arrives: a JSON
body, an event stream, a compressed body, a WebSocket turn, an error. A field
is anything AIntercept reads out of it: the model, each token count, the stop
reason, a tool call, the account, an identity. So a provider with three
shapes that yields eight fields has up to 24 cases.

The shared corpus of anonymized captures is issue #107. Until it lands, a new
provider adds its cases next to the code that reads them (`events/*_test.go`,
`convo/*_test.go`, `identity/*_test.go`), from captures with every real
identifier replaced (`scripts/check-real-data.py` fails on known ones; see
"Docs and tests" below).

## 1. Routing and TLS

| Where | What to add | Test |
|---|---|---|
| `deploy/helm/aintercept/values.yaml` `interception.allowlist`; `CERT_ALLOWLIST` in `docker-compose*.yml` and `deploy/kubernetes/certsvc.yaml` | The provider's hosts. certsvc mints a leaf for each. | `make up`, then a request through the proxy (`make verify`). |
| `envoy/envoy*.yaml` (local, and the source `deploy/kubernetes/envoy.yaml`'s ConfigMap is made from), `deploy/helm/aintercept/files/envoy*.yaml` (the chart's) | A filter chain (SNI) and an upstream cluster per host, with both ext_proc filters. | `internal/chart` tests render the chart. |
| The operator's intermediate CA | The hosts in its name constraints (`docs/PRODUCTION.md` §1). certsvc refuses to start when a host can't get a leaf clients accept. | certsvc's startup check. |
| `observer.pathProbe.targets` | A gentle path for a bot-protected web host (`host=host/cdn-cgi/trace`), and a concrete host for a wildcard entry. | `/healthz/path` on a running observer. |

## 2. Credentials and redaction (`aintercept/internal/events/events.go`)

| Where | What to add | Test |
|---|---|---|
| `extractAPIKey` (behind `HashAPIKey`, `KeyHint` and `APIKey`) | The header, query parameter or cookie that carries the credential. `key_hash` is its salted hash; nothing else of it is kept. | `events/gemini_key_test.go` is the pattern. |
| `sensitiveHeaders`, `RedactHeaders`, `RedactPath` | Every credential-bearing header, cookie and query parameter, so none reaches a row. | `extproc/gemini_redaction_test.go`. |
| `scripts/real-data-denylist.sha256` | The hashes of the real identifiers in the captures you worked from (accounts, orgs, devices, sessions, emails, IPs), so none can be committed. Credential shapes are gitleaks' job (`make check-repo`). | `make check-repo`. |

## 3. Identity (`aintercept/internal/identity`)

Who each credential belongs to: what the Interceptor's `Identity` carries
(`docs/INTERCEPTOR.md`, Identity) and what the `credentials` table holds. One
`Resolver` per credential type, registered in `Resolvers` (`resolvers.go`).
The cache, the callbacks and the pipeline never mention a provider.

A resolver has:
- **`Credential(host, headers)`**: the token it can look up, by host and the
  credential's shape (`sk-ant-oat…` is an Anthropic OAuth token). Match the
  host exactly: another provider's keys can share a prefix.
- **`Origin()` and `Lookup(ctx, client, origin, token)`**: one GET to the
  provider's own account endpoint, at a path fixed in the resolver. Never take
  any part of the URL from the request. Parse tolerantly (unknown fields
  ignored); a 200 that doesn't name the owner is `ErrShape`. Errors carry only
  a status, a timeout or `ErrShape`: never the token or the body.
- optionally **`Observe(event)`** (the `Observer` interface): the owner named
  by the provider's own account response that passed through the proxy.

What each credential resolves to today:

| Credential | Claims | Observe | Lookup | Resolves to |
|---|---|---|---|---|
| Anthropic OAuth (`sk-ant-oat…`, Claude Code) | — (opaque) | `/api/oauth/profile`, `/api/claude_cli/bootstrap` | `GET https://api.anthropic.com/api/oauth/profile`, `Authorization: Bearer` (the client's own call) | person (account, email, name) and org: `CREDENTIAL` |
| Anthropic API key (`sk-ant-api…`) | — | — | `GET https://api.anthropic.com/v1/organizations/me`, `x-api-key`, `anthropic-version: 2023-06-01` | the org; the person who created the key when the Anthropic admin directory is on (`observer.identityDirectory.anthropic.adminKeySecret`: the org's keys are listed with the Admin API, and a key whose `partial_key_hint` matches exactly one is attributed to its creator): `CREDENTIAL` |
| OpenAI API key (`sk-…` on `api.openai.com`) | — | — | `GET https://api.openai.com/v1/me`, `Authorization: Bearer` | person and default org: `CREDENTIAL` |
| ChatGPT / Codex (JWT bearer on `chatgpt.com`) | email, name, account (`events.AuthIdentityFromHeaders`) | — | `GET https://chatgpt.com/backend-api/wham/accounts/check` (Codex's own call), which lists the accounts a token ChatGPT accepts can reach | the claims, confirmed when the claims' account is listed: `CREDENTIAL`; else `CLAIMS` |
| Gemini API key | — | — | none: a key belongs to a GCP project, not a person | key hash and hint only |
| claude.ai (session cookie) | — | — | none yet | key hash and hint only |

Tests (`identity/resolvers_test.go`): for each lookup, success, 401, 403, an
unexpected shape and a timeout against a fake server; the resolver's
eligibility (host and shape); `Observe` from a complete row. Add the
provider's row to the table above and to `docs/harness-parity.md`.

## 4. Parsing and metadata (`aintercept/internal/events`)

| Where | What to add | Test |
|---|---|---|
| `usage.go` | The model (from the body, or the path: `ModelFromPath`), the token counts (`UsageFromResponseBody`) and stop reason, for each response shape: JSON, event stream (each provider's final event), compressed. | `events/usage_test.go`, one case per field per shape. |
| `extproc/sse.go`, `extproc/tail.go`, `extproc/ws.go`, `wsframe` | Streaming framing, if the provider streams other than SSE or WebSocket. | `extproc/sse_test.go`, `extproc/tail_test.go`, `extproc/ws_test.go`. |
| `request_summary.go` (`SummarizeRequest`, `ProductOf`), `meta.go` (`clientHeaders`, `providerHeaders`) | Client and product detection; the client's identifying headers and the provider's response headers worth keeping (`ProviderOrgID`). | `events/meta_test.go`. |
| `clientevents.go`, `otlp.go` | The client's own analytics, if it sends any (`IsAnalyticsRequest`, `ClientEventsFromRequest`). | `events/clientevents_test.go`. |

## 5. The Interceptor

| Where | What to add | Test |
|---|---|---|
| `aintercept/internal/convo` (`formatOf` in `convo.go`, a parser per format, `reply.go`) | The Interceptor: the request and reply shapes of the provider's model endpoints, so OnRequest, OnReply and OnToolCall see them. | `convo/convo_test.go`, `convo/reply_test.go`, `convo/toolcalls_test.go`. |
| `aintercept/internal/extproc/deny.go` | A refusal in the provider's own error shape (HTTP body, stream event, WebSocket message), so the client shows a sensible error. | `extproc/enforce_test.go`. |
| `aintercept/internal/e2e`, `test/interceptor/envoy.yaml`, `fixture/cmd/fixture/interceptor.go` | An end-to-end case through a real Envoy: the host in the Envoy config, the fixture serving the endpoint, a test in the Interceptor suite. | `make test-interceptor`. |

## 6. Pipeline, API and UI

What counts as a model call is defined in five places today. They must agree;
change all of them together (and make it one definition when you can):

- `aintercept/internal/canon/canon.go` `IsModelCall`
- `aintercept/internal/flush/flush.go` (uses `canon.IsModelCall`, and `agentCall`)
- `aintercept/internal/refresh/sql.go` `modelCall`, `agent`, `agentCall`
- `api/src/aintercept_api/drawer.py` `VISIBLE`
- `api/src/aintercept_api/sessionpages.py` `_MODEL_CALL`

Also:
- source and agent attribution: `refresh/sql.go` `agent`;
- the provider's UI states in `docs/ui/requirements.json`, with a story for
  each and a sample for each scenario (`make regen`; `docs/ui/BUILD_PLAN.md`).

## 7. Docs and tests

- `docs/TAXONOMY_<PROVIDER>.md`: every request kind and path the client sends,
  and what each lands as.
- A `docs/harness-parity.md` row for each client harness that uses the
  provider (what it sends, common / mapped / bespoke, what AIntercept shows).
- The provider lists in `README.md` and `docs/PRODUCTION.md`.
- A canon dataset for the API tests: `aintercept canon -src <capture> -raw
  <dir>`, then `canon -src <dir> -load <postgres>`.

## Provider done

A call through the proxy to the provider shows up with its model, its usage
and its identity, is recorded in the raw files and loaded into Postgres, and is
refused in the provider's own error shape in enforce mode: in `make verify`
and `make test-interceptor`, with every field covered by the test rule above.
