# Common cross-provider telemetry schema (v0)

Seeded from a real Claude Code capture (Anthropic `/v1/messages`). The goal is a
**single normalized record per intercepted LLM API transaction** that is the
same shape across providers, so tool usage can be queried uniformly — "which app
used which MCP server and which tools, on which model, when" — with
provider-specific details preserved in a side blob.

## Record

| field | type | notes |
|-------|------|-------|
| `schema_version` | string | `"0"` |
| `provider` | string | `anthropic` \| (future: `openai`, `google`, …) |
| `endpoint` | string | normalized path, e.g. `/v1/messages` (query stripped) |
| `ts` | int | unix ns of the request (from `request_headers` event) |
| `correlation_id` | string | stable per transaction; `REDACTED` in fixtures |
| `client.user_agent` | string | raw UA (redacted to `app/major.x` in fixtures) |
| `client.app` | string | derived family: `claude-code`/`claude-cli`, `anthropic-sdk`, `curl`, … |
| `model` | string | from request body, falling back to response `message_start` |
| `stream` | bool | whether the request asked for SSE streaming |
| `status` | int | upstream HTTP status |
| `tools_available[]` | list | tools DECLARED on the request (the catalog offered to the model) |
| `tool_calls[]` | list | tool calls the model issued THIS turn (from the response stream) |
| `tool_use_history[]` | list | every tool call across the conversation (from request history) |
| `usage` | object\|null | token counts from the response (`input_tokens`, `output_tokens`, cache fields) |
| `stop_reason` | string\|null | from the response |
| `provider_raw` | object | provider-specific fields not (yet) normalized |

### Tool entry (used by all three tool lists)

| field | type | notes |
|-------|------|-------|
| `name` | string | full tool name as sent, e.g. `mcp__playwright__browser_navigate` |
| `server` | string\|null | MCP server for `mcp__<server>__<tool>`; null for builtins |
| `tool` | string | tool name without the `mcp__server__` prefix |
| `kind` | string | `mcp` \| `builtin` \| `server` (provider-hosted, e.g. web_search) \| `unknown` |
| `id` | string | provider tool-call id (history/response only; dropped in fixtures) |
| `input` | object | tool arguments (history only; **dropped in fixtures** — contains content) |

## Tool attribution — the core value

A fully-qualified MCP tool name encodes its origin:

```
mcp__playwright__browser_navigate
└┬─┘  └───┬───┘  └──────┬───────┘
 kind     server         tool
```

This yields the "every tool in use by every server" view directly:
`report.py` groups `tool_use_history` by `server → tool` with counts, and maps
`app → {mcp servers}`. Builtins (Bash, Edit, Read, …) are `kind=builtin`,
`server=null`; provider-hosted tools (Anthropic `web_search`) are `kind=server`.

## Where each signal comes from (and a capture caveat)

- `tools_available`, `tool_use_history`, `model`, `stream`, `metadata`: the
  **request body** (plaintext JSON — Claude Code sends uncompressed).
- `tool_calls` (model's new calls), `usage`, `stop_reason`: the **response
  body**, which is a gzip'd SSE stream.
- **Caveat:** responses are compressed. The observer captures raw wire bytes as
  base64 (`body_b64`); the extractor base64-decodes then decompresses by
  `content-encoding`. An earlier capture stored bodies as a UTF-8 string, which
  corrupted the gzip — for that capture, response-side fields (`tool_calls`,
  `usage`) are empty, but `tool_use_history` from the request side still carries
  essentially all tool activity, because each turn echoes the full conversation.

## Provider mapping notes (for adding providers)

- **Anthropic** (`/v1/messages`): tools in `tools[]`; calls as `tool_use` content
  blocks (request history) and `content_block_start`/`input_json_delta` (SSE).
- **OpenAI** (`/v1/chat/completions`, `/v1/responses`): tools in `tools[]`
  (`function` type); calls in `tool_calls[]` / streamed `delta.tool_calls`.
  Map `function.name` → `name`; no `mcp__` convention, so MCP attribution will
  need a different source (e.g. a server registry) — a known gap to design for.
- **Google (Gemini)**: `tools[].functionDeclarations[]`; calls in
  `candidates[].content.parts[].functionCall`.

The common fields above are intended to hold for all three; anything that
doesn't map cleanly goes in `provider_raw`.
