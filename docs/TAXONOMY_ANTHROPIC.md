# Anthropic data taxonomy

What aintercept records for Anthropic traffic (`api.anthropic.com`), and how it
fits together. As of 2026-11-02, observer v14, Claude Code 2.1.308.
"Verified" means seen in live traffic through the DO cluster.

Privacy: the project is in experimentation mode, so some fields are kept
verbatim; see `docs/PRODUCTION.md` §8.

## 1. Where the data comes from

| Source | What it is | Lands as |
|---|---|---|
| The proxied connection | Every HTTP transaction: host, path, status, timing, caller IP | All rows |
| Model request/response bodies (`/v1/messages`) | Parsed in the observer; content discarded | Fields on `complete` rows |
| Request/response headers | Client, session, agent, account, rate-limit state | `client` / `provider` maps on `complete` rows; every header (secrets redacted) on header-phase rows |
| `metadata.user_id` in model requests | Claude Code's own identity triple | `user_id`, `device_id`, `account_uuid`, `session_id` |
| Claude Code analytics (`/api/event_logging/v2/batch`) | Claude Code's client telemetry: ~70 event types seen so far | One `client_event` row per event |
| Claude Code control-plane calls (`/api/oauth/*`, `/api/claude_cli/bootstrap`, `/mcp-registry/*`, …) | Settings, feature flags, account/plugin/skill lookups | `complete` rows (path, status, timing); bodies not parsed yet, shapes in `schema-samples/` |

## 2. Row types

| `phase` | One per | Use |
|---|---|---|
| `complete` | HTTP transaction | **The main analytic row.** Everything in §3. |
| `client_event` | Claude Code analytics event | Client-side behaviour (§5). Carries the delivering transaction's `ip`, `correlation_id`, `client` map. |
| `request_headers`, `response_headers` | transaction | All headers (secret values replaced by `<redacted>`). |
| `request_body_chunk`, `response_body_chunk` | body chunk | Byte counts only (full bodies only in dev capture builds). |
| `disconnect` | transaction | End-of-stream marker; its `error: context canceled` is normal, not a failure. |
| `ws_connection` | WebSocket connection | Summary of an upgraded connection (see `TAXONOMY_CODEX.md` §6); its model turns are `complete` rows with `transport: websocket`. Not seen for Claude Code yet. |
| schema sample (`schema-samples/` prefix) | new shape | Structure-only fingerprint of something not fully understood (§6). |

Filter `synthetic = true` out of everything: those are the observer's own
health probes.

## 3. `complete` row fields

### Who
| Field | Source | Notes |
|---|---|---|
| `ip` | TCP peer (via Envoy) | Per-device; requires a client-IP-preserving LB (verified) |
| `key_hash`, `key_hint` | `authorization` / `x-api-key` | Salted hash + masked hint. Claude Code OAuth tokens show as `sk-ant…` |
| `account_uuid`, `device_id`, `session_id`, `user_id` | `metadata.user_id` | Verified. `account_uuid` = the person's Anthropic account; `device_id` = per-machine hash |
| `provider_org_id` | `anthropic-organization-id` response header | Verified. Compare with your org for corporate vs personal |
| `provider["anthropic-workspace-id"]` | response header | API workspace |
| `client["x-organization-uuid"]` | request header | Sent on some Claude Code control-plane calls |

### Client, session, agent (`client` map, from request headers)
| Key | Example |
|---|---|
| `user-agent` | `claude-cli/2.1.308 (external, cli)` |
| `x-app` | `cli` |
| `x-claude-code-session-id` | session UUID (= `session_id`) |
| `x-claude-code-request-class` | `main`, `subagent` |
| `x-claude-code-agent-id`, `x-claude-code-agent-type` | subagent id, `Explore` |
| `x-claude-code-prompt-id` | one per user prompt (= analytics `cc_prompt_id`) |
| `x-client-request-id` | per request (= analytics `clientRequestId`) |
| `x-stainless-os`, `-arch`, `-lang`, `-package-version`, `-runtime`, `-runtime-version` | `MacOS`, `arm64`, `js`, `0.128.0`, `node`, `v26.3.0` |

### Model call
| Field | Source | Notes |
|---|---|---|
| `model` | request body (`model`) | |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | response `usage` (SSE or Brotli/gzip JSON) | Verified for Opus, Sonnet, Haiku |
| `service_tier`, `inference_geo`, `speed` | response `usage` | e.g. `standard`, `global`, `standard` |
| `stop_reason` | response | `tool_use`, `end_turn`, `max_tokens`, `refusal`, … |
| `stream`, `max_tokens`, `effort`, `thinking_type` | request body | `effort` from `output_config.effort` |
| `message_id` | response | Chains turns (below) |
| `previous_message_id`, `thread_type` | request `thread` | Claude Code message threads: `continue` requests reference the previous response's `message_id` |

### Attachments
`attachments` counts images and files sent with a model request (Anthropic
`image` / `document` blocks, inline base64 or by reference): `images`,
`files`, `inline_bytes`, `by_reference`. Claude Code resends history on plain
message requests, so an attachment counts again on later turns there; with
message threads it counts on the turn that adds it.

### Tools
| Field | Source | Notes |
|---|---|---|
| `tool_uses` | **response** `tool_use` / `server_tool_use` / `mcp_tool_use` blocks | Tools the model called in this response, each once. Works for every client. Verified (MCP: `claude_ai_RCP`, `claude_ai_Vercel`) |
| `tool_calls` | request history `tool_use` blocks | Older/full-history clients only; re-counts history each turn |
| `tools_available` | request `tools` | Full catalog; with message threads only on a thread's first request |

Each tool entry has `name`, `server` (MCP server), `tool`, `kind`
(`mcp` / `builtin` / `server`).

### Account limits (`provider` map, from response headers)
`anthropic-ratelimit-unified-status`, `-5h-utilization`, `-7d-utilization`,
`-representative-claim`, `-fallback-percentage`, `-overage-status`,
`request-id`.

### Transaction
`host`, `method`, `path`, `status`, `timestamp_ns`, `correlation_id`,
`observer_instance`, `synthetic`.

## 4. How the identifiers join

```
account_uuid ─┬─ device_id ── session_id ── cc_prompt_id (user prompt) ── request ── message_id
 (person)     │  (machine)    (Claude Code   = x-claude-code-prompt-id    ─ previous_message_id → prior turn
 org_uuid ────┘                session)                                   ─ requestId = provider["request-id"]
```

- `session_id`: `metadata.user_id`, `x-claude-code-session-id`, and every `client_event`.
- `cc_prompt_id` (analytics) = `x-claude-code-prompt-id` (header): all requests and events caused by one user prompt.
- `message_id` → next request's `previous_message_id`: reconstructs a conversation turn by turn, content-free.
- Analytics `requestId` = `provider["request-id"]`; analytics `clientRequestId` = `client["x-client-request-id"]`: joins a `client_event` (e.g. its `costUSD`) to the exact `complete` row.
- `account_uuid` / `organization_uuid` (analytics `auth`) = `account_uuid` / `provider_org_id`.

Verified on captured traffic: 29/29 `tengu_api_success` events matched a
proxied response by `request-id` and by client request id; 2/2 prompt ids
matched.

## 5. Claude Code analytics (`client_event` rows)

Columns: `client_event.name`, `.type` (`ClaudeCodeInternalEvent`), `.id`,
`.timestamp`, `.session_id`, `.device_id`, `.account_uuid`,
`.organization_uuid`, `.model`, `.agent_id`, `.agent_type`, `.index` (the
event's place in its batch, 1-based: two identical events in one batch are still
distinct rows), and `.data`: the full event, with `additional_metadata` and
`process` decoded from base64.

**On every event** (`data`): `env` (Claude Code version and build time,
platform, arch, shell, terminal, Node version, package managers, VCS, CI /
GitHub Action / remote / conductor flags, claude.ai auth), `head_sha` (git
commit of the working repo), `is_interactive`, `user_type`, `betas`,
`process` (rss, heap, CPU, uptime), and in `additional_metadata`:
`subscription_type` (e.g. `max`), `cc_prompt_id`, `renderer_mode`.

Event types seen so far, by theme (`am.` = `additional_metadata.`):

| Theme | Events | Notable fields |
|---|---|---|
| **Model calls and cost** | `tengu_api_query`, `tengu_api_success`, `tengu_fork_agent_query`, `tengu_api_cache_breakpoints` | `am.costUSD`, tokens incl. 1h/5m cache writes, `ttftMs`, `durationMs`, retries, `querySource`, `permissionMode`, which MCP server/tool or agent triggered it (`attributionMcpServer`, `attributionMcpTool`, `attributionAgent`), message/system/tools sizes, `toolsCount`, `requestId` |
| **Tools and MCP** | `tengu_tool_use_success`, `_progress`, `_granted_in_config`, `_can_use_tool_allowed`, `tengu_tool_search_mode_decision`, `tengu_deferred_tools_pool_change`, `tengu_mcp_instructions_pool_change` | tool name, `mcpServerName`, `mcpServerId`, `mcpServerType` (`claudeai-proxy`), hashed `mcpServerBaseUrl`, duration, input/result sizes, read-only hints, permission wait |
| **Shell** | `tengu_bash_tool_command_executed`, `tengu_bash_ast_too_complex`, `tengu_shell_set_cwd` | program (`bash_argv0`), command class, exit code, pipes/heredoc/subshell, `destructive_category`, `git_destructive_target`, sandbox state and violations, `dangerously_disable_sandbox`, stdout/stderr length |
| **Files and git** | `tengu_file_operation`, `tengu_file_changed`, `tengu_file_activity`, `tengu_file_read_reread`, `tengu_file_history_*`, `tengu_git_operation`, `tengu_dir_search` | operation (`write`, `commit`), tool, **hashed** file path and content, lines added/removed, `fileExtension` (can hold command fragments) |
| **User input** | `tengu_input_prompt`, `tengu_paste_text`, `tengu_prompt_suggestion`, `tengu_keybinding_fired`, `tengu_return_to_session` | prompt length and index, `prompt_source` (`typed`), **`is_negative`** (frustration/negative signal), `is_keep_going`, pasted bytes, time away |
| **Agents** | `tengu_agent_tool_selected`, `tengu_agent_tool_completed`, `tengu_agent_summary_stopped` | agent type, model, effort, tokens, tool uses, duration, prompt/response char counts |
| **Turns and latency** | `tengu_turn_end`, `tengu_turn_first_text`, `tengu_attachments`, `tengu_attachment_compute_duration`, `tengu_context_announcement` | turn duration, error flag, end reason, time to first text, requests/tool calls before first text |
| **Hooks and features** | `tengu_run_hook`, `tengu_repl_hook_finished`, `tengu_feature_ok` | hook names (hashed suffix), blocking/error counts, which features fired (auto-approve, model routing) |
| **Auth** | `tengu_oauth_token_refresh_*`, `tengu_oauth_tokens_saved` | token refresh lifecycle, plugin scopes |
| **Memory and skills** | `tengu_memdir_*`, `tengu_skills_sync_*` | memory file writes/accesses, skill sync |
| **Client health** | `tengu_event_loop_stall`, `tengu_spinner_stalled_ui`, `tengu_render_*`, `tengu_notification_method_used` | stalls, memory, render path, terminal |
| **Thread/caching internals** | `tengu_tether_*`, `tengu_wire_*`, `tengu_reminder_fold_*`, `tengu_context_rendering_recorded`, `tengu_inline_tools_announced`, `tengu_declared_tool_set_held`, `tengu_cache_eviction_hint`, `tengu_bg_classify` | How Claude Code manages message threads and caching; low dashboard value |

The full list of known events and their field paths is the registry,
`aintercept/internal/events/client_events/<client>.json`.

## 6. Tracking what changes

| Mechanism | Catches | Where it shows |
|---|---|---|
| Request-shape sampling | Responses/requests without a model or usage (`no_model`, `no_usage`), model calls with no conversation (`no_conversation`) or no tool catalog (`no_tools`) | `schema-samples/`, `make schema-report` |
| Client-event registry | New analytics event names (`new_client_event`) or new fields on known events (`new_client_event_fields`) | Row flags `client_event.new_event` / `.new_fields`; `schema-samples/` path `…/batch#<event>`; `make client-event-registry` to absorb |

Both store structure only (keys and value types), never values.

## 7. Claude web (claude.ai, and the desktop app that wraps it)

Verified with Chrome through DO. claude.ai is behind Cloudflare bot
management; see `docs/INTERCEPTION_ISSUES.md` B1-B4.

### Turn row (`POST /api/organizations/<org>/chat_conversations/<conv>/completion`, `web_chat.app: claude`)
The response is Anthropic Messages SSE plus claude.ai events, so the
standard fields fill as they do for the API: `model`, `tool_uses` (`view`,
`bash_tool`, `create_file`, `Artifact`, connector tools, …), `stop_reason`,
`message_id`, and `tools_available` (33 tools, **including the user's
connectors**). **It has no token counts.** Added:

| Field | Source |
|---|---|
| `web_chat.org_id`, `.conversation_id` | URL path |
| `web_chat.requested_model`, `effort`, `web_chat.thinking_mode` | request `model`, `effort`, `thinking_mode` |
| `previous_message_id` | request `parent_message_uuid` |
| `attachments` | `files[]` (uploaded, by reference) + `attachments[]` (text extracted in the browser; `inline_bytes` = their sizes) |
| `web_chat.limits` | `message_limit` event: 5h / 7d window utilization and status |

### Uploads (`POST /api/organizations/<org>/conversations/<conv>/wiggle/upload-file`)
`web_chat.upload` gives `name`, `size`, `mime_type`, `kind` (blob / image / document) and `file_id`; the multipart body holds the bytes.

### Analytics (`client_event`, `source: claude_web`)
claude.ai posts the same `/api/event_logging/v2/batch` format as Claude Code
with its own events (35 registered), e.g. `claudeai.message.sent`,
`claudeai.message.perceived_ttft` / `perceived_ttlt`,
`claudeai.chat.file_upload.added`, `claudeai.frame_artifact.pane.opened`,
`claudeai.mcp.bootstrap_stream.completed`, `claudeai.chat.message_queue.*`,
`login.google.*`, `page_viewed`, `$identify`. Before this source existed
these rows were filed as `claude_code`; the registry script reassigns them by host.

### Identity
claude.ai authenticates with an opaque `sessionKey` cookie. Its hash is the
row's `key_hash` (stable for the browser session), and `auth` (`email`,
`name`, `account_id` = account uuid, `user_id` = tagged id) is set on the
account responses (`/api/account`, `/edge-api/bootstrap/<org>/app_start`,
login). **To attribute any claude.ai row, join it by `key_hash` to an
account row.** The org UUID is in every path, and analytics events carry
`auth.account_uuid` / `organization_uuid`.

### RUM (`client_event`, `source: claude_web`, `type: rum`)
`/api/v2/rum` is claude.ai's Datadog browser RUM, proxied first-party. Events
are named `view`, `action:<type>` (clicks and custom actions), `resource:<type>` (fetch,
js, css, document, …, with timings), `error`, `long_task` and `vital:<name>`,
and carry `session_id` and `user_id`. It's high volume: about 10k events per
session, mostly `resource:js`.

### Also intercepted
`a.claude.ai`: Claude web's Segment analytics iframe. Captured, not parsed yet.

## 8. Not covered yet

- **Control-plane bodies** (`/api/oauth/profile`, `/api/claude_cli/bootstrap`, `/mcp-registry/*`, `/v1/mcp_servers`, plugin/skill lists): recorded as transactions, bodies not parsed.
- **MCP server traffic itself**: we see the model calling an MCP tool (`tool_uses`, analytics), not the MCP server's own network calls.
- **Tool catalog per session**: `tools_available` only on a thread's first request; combine per `session_id`.
- **Claude desktop app**: expected to behave like claude.ai (it wraps it); not yet run through the proxy.
