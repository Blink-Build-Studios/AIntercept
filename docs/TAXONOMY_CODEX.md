# OpenAI / Codex data taxonomy

What aintercept records for OpenAI Codex, as of 2026-11-02 (observer v18,
Codex CLI 0.164.0 on a ChatGPT login, `codex exec`). Everything here was
verified on live traffic through the DO cluster, from runs covering a coding
loop with failing tests, subagents, image input, `gpt-6-astra` at high effort,
`exec resume`, `exec review`, and web search. Companion to
`TAXONOMY_ANTHROPIC.md`. Row types, identifiers and change tracking work the
same way across both.

Privacy: experimentation mode, so some fields are kept verbatim; see
`PRODUCTION.md` §8.

## 1. Where the data comes from

Codex contacts exactly two hosts (found by logging every outbound
connection), and both are intercepted:

| Host | Endpoint | What it is | Lands as |
|---|---|---|---|
| `chatgpt.com` | `GET /backend-api/codex/responses` (WebSocket, `101`) | **Model calls**: one connection carries every turn of a session | `complete` rows per turn (§3), `ws_connection` row (§4) |
| | `POST /backend-api/codex/responses` | Same over HTTP (zstd request, SSE response); Codex's fallback | `complete` rows |
| | `POST /backend-api/codex/analytics-events/events` | **Codex analytics events** | `client_event` rows, `source: codex` (§6) |
| | `POST /backend-api/codex/alpha/search` | **Web-search service** (queries in, ranked results out) | `complete` row with `web_search` (§3) |
| | `POST /backend-api/ps/mcp` | MCP JSON-RPC to OpenAI-hosted apps | `mcp` field (§5) |
| | `GET /backend-api/codex/models`, `/ps/plugins/list` (paginated, ~25 pages per start), `/ps/plugins/installed`, `/ps/plugins/suggested/codex`, `/plugins/featured`, `/wham/accounts/check`, `/wham/settings/user` | Control plane: model list, plugin catalog, account and settings | `complete` rows (path, status, timing); bodies not parsed |
| `ab.chatgpt.com` | `POST /otlp/v1/metrics` | **Codex OpenTelemetry metrics** (OTLP/HTTP JSON, 0.7–1.2 MB per export, `statsig-api-key` header) | `client_event` rows, `source: otel` (§7) |

**File uploads** travel inside the model protocol, not through a separate
upload: an image attached with `codex exec -i` is sent inline in
`response.create` as an `input_image` part with a `data:image/png;base64,…`
URL. aintercept records counts and sizes (`attachments`, §3), and Codex's own
turn event records the image's dimensions (`image_preparations[]`, §6).

## 2. Row types

| `phase` | One per | Notes |
|---|---|---|
| `complete` | HTTP transaction, or **model turn on a WebSocket** (`transport: websocket`, `ws_turn` 1-based; `correlation_id` = the connection) | The main analytic row |
| `ws_connection` | WebSocket connection, on close | §4 |
| `client_event` | Codex analytics event, or OTel metric per export | §6, §7 |
| `request_headers` / `response_headers` / body chunks / `disconnect` | transaction | As for Anthropic |

Filter `synthetic = true` (health probes; they also hit `ab.chatgpt.com /`)
and `prewarm = true` (Codex's warm-up turn at the start of each connection:
`generate: false`, ~2 input tokens, no output).

## 3. Model-call row (`complete`)

### Who
| Field | Source | Example / notes |
|---|---|---|
| `ip` | TCP peer | per device |
| `account_uuid` | `chatgpt-account-id` header | ChatGPT account |
| `device_id` | turn metadata `installation_id` | per Codex installation |
| `session_id` | turn metadata `session_id` | = `thread_id` of the root thread |
| `key_hash`, `key_hint` | `authorization` (ChatGPT OAuth token) | salted hash + hint |
| `provider["x-codex-plan-type"]` | response header, or `codex.rate_limits` in-band on WebSockets | `plus`, … |

### `client_metadata` (decoded `x-codex-turn-metadata`, per turn)
`installation_id`, `session_id`, `thread_id`, `turn_id`, `root_turn_id`,
`window_id`, `context_window_id`, `agent_name` (`/root`, or a subagent's),
`request_kind`, `turn_trigger` (`exec`), `thread_source`,
`history_initialization`, **`sandbox` / `sandbox_mode`**
(`danger-full-access`, …), `auto_review_enabled`, **`workspaces`**
(working-directory paths and whether they have changes), `model`,
`reasoning_effort`, `analytics_enabled`, `turn_started_at_unix_ms`.

### Model call
| Field | Source | Example |
|---|---|---|
| `model` | request, else response | `gpt-6.1-sol`, `gpt-6-astra` |
| `effort` | request `reasoning.effort`, else response | `low`, `medium`, `high` |
| `stream`, `prewarm` | request (`generate: false` → prewarm) | |
| `service_tier` | response | `auto`, `default` |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `reasoning_tokens` | `response.completed` usage | cached = `input_tokens_details.cached_tokens`; reasoning when reported |
| `stop_reason` | terminal event status | `completed`, `incomplete:interrupted`, `failed` |
| `message_id`, `previous_message_id` | response id; request `previous_response_id` | chains turns (`resp_…`) |
| `attachments` | request input parts | `{images: 1, inline_bytes: 105}`; also `files`, `by_reference` |

### Tools
| Field | Source | Notes |
|---|---|---|
| `tool_uses` | response items (`custom_tool_call`, `function_call`, `mcp_call`) **plus the tools an `exec` script calls** (`tools.<name>(…)`) | Codex runs model-written scripts through one `exec` tool; MCP, shell, patch and search calls happen inside them. Seen: `exec`, `exec_command`, `apply_patch`, `web__run`, `fs/list_directory`, `fs/read_text_file`, `fs/read_multiple_files`, `fs/write_file` |
| `tool_calls` | same, over request history | re-counts history each turn |
| `tools_available` | `input[].type = additional_tools` (namespaces), on a connection's first turn | `exec`, `wait`, `request_user_input`, `clock.sleep`, `collaboration.spawn_agent`, … (MCP tools are exposed inside `exec`, not listed) |

### Web search (`web_search`, on `/alpha/search` rows)
`queries`, `results`, `result_domains` (distinct sites returned, e.g.
`go.dev`, `github.com`, `en.wikipedia.org`, `www.reddit.com`),
`external_web_access`. Query text and snippets are not kept.

### Usage limits (`provider`)
`x-codex-plan-type`, `x-codex-primary-used-percent` / `-secondary-`,
`-window-minutes`, `-reset-at`, `x-codex-active-limit`, `x-codex-credits-*`.
On WebSockets these come in-band (`codex.rate_limits`) and fill the same keys.

## 4. WebSocket transport

One connection per `codex exec` session (and per subagent). Messages seen
across the runs:

| Direction | Types |
|---|---|
| client → server | `response.create` (one per turn), `response.interrupt` |
| server → client, per turn | `response.created`, `response.in_progress`, `response.output_item.added` / `.done`, `response.content_part.added` / `.done`, `response.output_text.delta` / `.done`, `response.custom_tool_call_input.delta` / `.done`, `response.function_call_arguments.delta` / `.done`, `response.metadata`, `response.completed` / `response.incomplete`, `response.interrupt.accepted`, `response.output_item.interrupted` |
| server → client, Codex-specific | `codex.rate_limits`, `codex.response.metadata` (response headers in-band), `responsesapi.websocket_timing` |

`response.create` carries `model`, `stream`, `input`, `tool_choice`,
`parallel_tool_calls`, `reasoning`, `store`, `include`, `prompt_cache_key`,
`text`, `client_metadata`, `previous_response_id`, `generate`,
`access_programs`. Input item types: `message`, `additional_tools`,
`custom_tool_call` / `_output`, `function_call` / `_output`,
`agent_message` (subagent traffic), `reasoning`. Content parts:
`input_text`, `input_image`, `output_text`, `encrypted_content`.

`ws_connection` row: `websocket.duration_ms`, `bytes_up` / `bytes_down`,
`messages_up` / `messages_down`, `turns`, `close_code` (usually empty: Codex
drops the connection without a close frame), `deflate`, `undecodable`,
`message_types` (counts by `<direction>:<type>`), plus identity.

## 5. Hosted MCP (`mcp` field)

`/backend-api/ps/mcp` is MCP JSON-RPC to OpenAI's hosted apps: `method`
(`initialize`, `tools/list`, `tools/call`), `tool` for `tools/call`,
`tools_listed` (111 tools, e.g. `sites.create_site`), `error_code`.

## 6. Codex analytics (`client_event`, `source: codex`)

**On every event:** `thread_id`, `session_id`, `parent_thread_id`,
`subagent_source`, `thread_source`, `app_server_client` (client name, version,
transport), `runtime` (Codex version, OS, OS version, arch). Tool events also
carry `turn_id`, `root_turn_id`, `item_id`, `originating_response_id` /
`subsequent_response_id` (the model calls around them), timings,
`terminal_status`, `failure_kind`, and approval data
(`final_approval_outcome`, review counts, requested permissions and network
access).

| Event | Notable fields |
|---|---|
| `codex_turn_event` | per-turn `input_tokens`, `cached_input_tokens`, `cache_write_input_tokens`, `output_tokens`, `reasoning_output_tokens`, `model`, `model_provider`, `reasoning_effort`, `service_tier`, **`approval_policy`**, **`sandbox_policy`**, `sandbox_network_access`, counts of tool / MCP / shell / web-search / subagent / file-change / image calls, `status`, `turn_error`, `codex_error_kind`, sampling and tool-blocking timings, `usage_limit_window_minutes`, `personality`, `collaboration_mode`, `workspace_kind`, `active_plugin_ids_at_turn_start[]`, **`image_preparations[]`** (source and prepared width/height, detail) |
| `codex_mcp_tool_call_event` | **`mcp_server_name`, `mcp_tool_name`**, `connector_id`, `plugin_id`, duration, `mcp_error_present` |
| `codex_command_execution_event` | command source, **`exit_code`**, `sandbox_backend`, duration, read/search/list action counts |
| `codex_file_change_event` | **file add / update / delete / move counts**, `tool_name` (`apply_patch`) |
| `codex_accepted_line_fingerprints` | **`accepted_added_lines`, `accepted_deleted_lines`** per turn, `model_slug`, `product_surface`, `line_fingerprints[]`, `repo_hash` (empty in these runs) |
| `codex_collab_agent_tool_call_event` | subagent calls: `sender_thread_id`, `receiver_thread_ids[]`, agent counts (completed / failed / state), `requested_model`, `requested_reasoning_effort` |
| `codex_web_search_event` | `query_count`, `query_present`, `web_search_action`, duration |
| `codex_dynamic_tool_call_event` | built-in tool name, success, output item counts |
| `codex_control_tool_call_event` | control tools (e.g. `view_image`), success |
| `codex_thread_initialized` | `model`, `initialization_mode`, `is_worktree`, `ephemeral`, fork / parent ids |

Known events and fields: `aintercept/internal/events/client_events/codex.json`.

## 7. Codex OpenTelemetry metrics (`client_event`, `source: otel`)

One row per metric per export: `name`, `data.kind` (`sum` / `histogram` / …),
`data.unit`, `data.resource` (`service.name` = `codex_exec`,
`service.version`, `os`, `os_version`, `env`), and `data.points[]` (each with
`attributes`, and `value` or `count`/`sum`/`min`/`max`/buckets, timestamps).
Most points carry `model`, `auth_mode`, `app.version`, `originator`,
`session_source` (`exec`, or `subagent_thread_spawn_<id>` for subagents).
**There is no user identity in the metrics**; attribute them through the
delivering connection (`ip`) and the session's other rows.

66 metrics seen, by area:

| Area | Metrics (`codex.` prefix) |
|---|---|
| Turn performance | `turn.ttft.duration_ms`, `turn.ttfm.duration_ms`, `turn.e2e_duration_ms`, `turn.tool.call`, `turn.memory`, `turn.network_proxy`, `turn.unified_exec.running_processes`, `guardian.turn.blocking_ms` |
| WebSocket transport | `websocket.event` (+ `.duration_ms`, by `kind`), `websocket.request` (+ `.duration_ms`), `websocket.continuation` |
| Agents | `multi_agent.spawn`, `multi_agent.spawn.phase.duration_ms`, `multi_agent.wait.duration_ms`, `multi_agent.result_delivery` |
| Tools | `tools.registered`, `tools.incremental_updates`, `tool.unified_exec` |
| MCP | `mcp.protocol_discovery` (+ `.duration_ms`), `mcp.tools.list.duration_ms`, `mcp.tools.fetch_uncached.duration_ms`, `mcp.tools.cache_write.duration_ms`, `mcp.tools.cache_publish.duration_ms`, `mcp.binding_catalog.raw_definition_json_bytes`, `apps.refresh.duration_ms` |
| Skills and plugins | `skills.shadow_selection` (+ catalog / selected entries, query terms, reduction, duration), `thread.skills.*` (enabled, kept, truncated), `plugins.loaded_cache.*` |
| Web search | `web_search.results.payload_bytes` |
| Sessions | `thread.started`, `rollout.size_bytes`, `rollout_compression.*`, `thread_history.sqlite_projection` |
| Startup and client health | `process.start`, `startup.phase.duration_ms`, `startup_prewarm.*`, `sqlite.init.*`, `auth_storage.*`, `shell_snapshot.*`, `cloud_config_bundle.*`, `remote_models.*` |

Known metrics and attributes: `aintercept/internal/events/client_events/otel.json`.
The OTLP parser is generic (any client exporting OTLP/HTTP JSON metrics).

## 8. How the identifiers join

```
account_uuid (chatgpt-account-id) ── device_id (installation_id) ── session_id / thread_id ── turn_id ── model call (resp_…)
```

- `session_id` / `thread_id`: turn metadata, `session-id` / `thread-id` headers, every analytics event.
- `turn_id` / `root_turn_id`: turn metadata on each model call and analytics tool and turn events.
- Analytics tool events' `originating_response_id` = the `message_id` of the model call that requested the tool (verified 7/7).
- Subagents: their own threads (`session_source: subagent_thread_spawn_<id>` in metrics; `sender_thread_id` / `receiver_thread_ids` in collab events).

## 9. Change tracking

Unchanged from Anthropic: unknown analytics events or fields, and unknown
OTel metrics or attributes, are flagged on their rows and sampled to
`schema-samples/`. In these runs the registry flagged 5 new Codex events,
`image_preparations[]` and a new metric automatically; `make
client-event-registry` absorbed them.

## 10. ChatGPT web (browser)

ChatGPT in a browser uses the same `chatgpt.com` host as Codex, plus
`ws.chatgpt.com` (a per-user WebSocket for resuming conversations) and
`*.oaiusercontent.com` (file bytes). Verified with Chrome through DO.

### Turn row (`POST /backend-api/f/conversation`, `web_chat.app: chatgpt`)
The response is SSE in OpenAI's own `delta_encoding: v1` format (patches to a
message tree), not the Responses API. **It has no token counts.**

| Field | Source | Example |
|---|---|---|
| `model` | `server_ste_metadata.model_slug`: the model that served | `gpt-6`, `gpt-6-mini` |
| `web_chat.requested_model` | request `model` | `auto` |
| `web_chat.conversation_id`, `message_id`, `previous_message_id` | request and stream | |
| `web_chat.plan_type`, `.surface`, `.trigger` | metadata, `client_contextual_info.app_surface`, `turn_attribution` | `free`, `codex_browser`, `composer` |
| `web_chat.search_calls`, `.title`, `.limits` | `search_tool_call_count`, `title_generation`, `limits_progress` | `[{feature_name: reason, remaining: 5}]` |
| `web_chat.server_metadata` | `server_ste_metadata`, verbatim | region, `server_ttfvt_ms`, `did_auto_switch_to_reasoning`, `tool_name`, … |
| `tool_uses` | tool messages (`author.role: tool`), one per message | `web.run`, `python`, `functions.exec`, `api_tool.call_tool`, image generation (obfuscated name) |
| `attachments` | `messages[].metadata.attachments` (by reference) | `{images: 1, files: 1, by_reference: 2}` |
| `auth` | the bearer JWT's claims | `email`, `name`, `user_id`, `account_id`, `plan` |

### File uploads
1. `POST /backend-api/files` → `web_chat.upload` (`name`, `size`, `mime_type`, `use_case`, `file_id`).
2. `PUT sdmntpr<region>.oaiusercontent.com/files/<id>/raw`: the bytes, captured through the wildcard host.
3. `POST /backend-api/files/process_upload_stream`: processing progress.

### Analytics (`client_event`, `source: chatgpt_web`)
- **`/ces/v1/rgstr` (Statsig).** Every event carries the signed-in user:
  `userID`, **`email`**, account, device, plan, region and app version (promoted to
  `client_event.email` / `user_id` / `account_uuid` / `device_id`).
  - Structured product events are named by protobuf type (33 seen), e.g.
    `ChatgptTurnStart`, `ChatgptConversationTurnTurnExchangeStarted`,
    `ChatgptFileUploadInitiated`, `ChatgptFileUploadCreateFileEntry`,
    `ChatgptContentReferenceInteraction`, `ChatgptAdsPlacementLifecycleEvent`,
    `ChatgptSubscriptionUpsellInteraction`, `CodexPerformanceSpanCompleted`,
    `CodexUsageTransportMetric`, `AccessFlowSuccess` (login method).
  - Statsig's own events: `statsig::gate_exposure`, `layer_exposure` and
    `config_exposure`, which show the feature flags and experiments the user is in.
- **`/ces/v1/telemetry/intake`:** turn logs, e.g. `turn-analytics:Turn exchange complete`, latency observations, permission queries.
- **`/ces/statsc/flush`:** counters and histograms, named `<namespace>.<metric>` (e.g. the `default.chatgpt.model_written_dil.v2.*` render metrics).
- **`bzr.openai.com`** (ads/attribution sync) is intercepted and captured, but not parsed yet. ChatGPT's Datadog RUM goes to a third-party host and is opt-in (see `docs/INTERCEPTION_ISSUES.md` C2).
- **Session identity without a bearer token:** ChatGPT's next-auth session cookie feeds `key_hash`.

90 event names are registered; new ones are flagged like Codex's.

## 11. Not covered yet

- **`api.openai.com` with an API key** (OpenAI SDKs, Codex in API-key mode): intercepted and parsed by the same Responses / Chat extractors, but not exercised in these runs.
- **ChatGPT desktop and the Codex desktop app**: not routed through the proxy yet; they may use other hosts and endpoints.
- **Control-plane bodies** (plugin catalog, model list, account and settings): recorded as transactions, not parsed.
- **Local (stdio) MCP servers' own traffic**: only what Codex reports (tool uses, analytics).
- **Browser deployment obstacles** (bot checks, ECH, QUIC): tracked in `docs/INTERCEPTION_ISSUES.md`.
