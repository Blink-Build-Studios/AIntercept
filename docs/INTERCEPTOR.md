# The Interceptor: plugging a control plane into AIntercept

AIntercept sees every AI call that passes through it. The **Interceptor** is
how you act on what it sees. You run a gRPC service that implements
`aintercept/interceptor/v2/interceptor.proto`. AIntercept calls it with every
message of every call, in both directions, before it goes on: the prompts and
tool results going to the model, and the model's reply text and tool calls
coming back. Depending on the mode, it records or applies what your service
decides.

- **One integration, two modes.** Observe and enforce use the same callbacks
  and the same messages. Moving a host from observe to enforce is a deployment
  setting, never a rewrite.
- **One shape for every provider.** Each call's new messages arrive the same
  way for Claude Code, Codex (HTTP or WebSocket) and the OpenAI APIs, and
  AIntercept puts your answer back on the wire in the provider's format.
- **Writing one?** In Go, use package `intercept` (rules, hooks and watches
  over this protocol): [`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md).
  In any other gRPC language, generate a server from the proto (below,
  "Writing one in another language").
  [`BUILDING_AN_INTERCEPTOR.md`](BUILDING_AN_INTERCEPTOR.md) walks through
  wiring it into a deployment: local testing through a real Envoy, packaging,
  Helm values, and the observe-then-enforce rollout. This document is the
  protocol.
- **Examples:** `aintercept/examples/rabbit` and `aintercept/examples/redact`
  (Go, package `intercept`), and `aintercept/examples/python-redact` (Python,
  the raw protocol).

Status: **built for Claude Code and Codex** (Anthropic Messages, OpenAI
Responses over HTTP and Codex's WebSocket, Chat Completions): every message
in both directions, before it goes on. The request's new messages, Replace,
Fetch, Respond and Error; the reply's text piece by piece and its tool calls
whole, each decided before the client sees it; and, without content, each
call's provider settings (options: effort, fast mode, the priority tier),
decided before it goes upstream. Not yet: ChatGPT and claude.ai on the web,
whose calls get `OnCallStart` and `OnCallComplete` and go upstream unread.

The first contract, v1, handed an Interceptor whole request bodies and each
streamed event (`OnMessage`). It has been removed: v2 is the only contract,
and an Interceptor written for v1 has to be ported (in Go, its rules are a few
lines of package `intercept`).

## Why messages, not the wire

Handing an Interceptor the wire (the whole request body, then each streamed
event) is wrong for an integrator in three ways:

- **Every call resends the conversation.** Claude Code and Codex send the whole
  history on each call, so an Interceptor sees (and must re-check) every
  earlier message again, and a long session's body is several MiB per call.
  A rule on "what the user said" fires again on every later call unless the
  Interceptor works out which message is new, per provider.
- **The integrator has to know every provider's format.** Anthropic Messages,
  OpenAI Responses (over HTTP and Codex's WebSocket), ChatGPT's and claude.ai's
  web formats, each with its own streaming events.
- **Streamed text arrives in pieces.** A phrase can span two events.

AIntercept does all of that instead. An Interceptor sees **only the new
messages** of each call, **in one shape for every provider**, decides on each,
and AIntercept turns the decision back into that provider's format.

## The model

A **message** is one turn of the conversation: the user's prompt, the model's
reply, a tool call, a tool result.

```proto
message ConvMessage {
  string id = 1;              // stable: AIntercept's id for this message in this session
  Role role = 2;              // USER, ASSISTANT, TOOL, SYSTEM (the harness)
  repeated Part parts = 3;
  int32 index = 4;            // position in the conversation, from 0
}

message Part {
  oneof kind {
    Text text = 1;            // prompt or reply text
    Media media = 2;          // an image or a file
    ToolCall tool_call = 3;   // the model asks to run a tool: name, arguments (JSON)
    ToolResult tool_result = 4;
    Opaque opaque = 5;        // reasoning, encrypted content: present, never inspectable
    ToolAdded tool_added = 6; // a tool the harness added mid-session: its name
  }
  string id = 10;             // for Fetch and Replace
}
```

**Large content goes by reference.** A part over the inline limit (default
64 KiB; a deployment setting) arrives without its content: its type, size,
media type and a preview (the first 1 KiB of text, or nothing for binary). If
a rule needs the rest, the Interceptor asks for it (Fetch, below). A call's
inline content is also capped as a whole (3 MiB): past it, the largest parts go
by reference, so many parts each under the limit can't add up to a large
message. Messages therefore stay under gRPC's default 4 MiB whatever the
conversation holds, and an Interceptor that never fetches needs no limit
changes.

## What's new in a call

AIntercept works this out per call, without keeping state:

| Client / API | The request chains to a previous response | Otherwise |
|---|---|---|
| Anthropic Messages (Claude Code, the SDKs) | Claude Code's message threads (`thread`, `previous_message_id`): every message in the request is new | the messages after the last `assistant` message; when the request ends with an `assistant` message (a prefill), that message and the prompt before it |
| OpenAI Responses (Codex over HTTP or WebSocket, the SDKs) | `previous_response_id`: every input item is new | the input items after the last output item (an assistant message, `reasoning`, or any `*_call` item) |
| OpenAI Chat Completions | n/a | the messages after the last `assistant` message |
| ChatGPT on the web | the request carries only the new message (the server keeps the conversation) | |
| claude.ai | the request carries only the new prompt and its attachments | |

The new messages of a request are usually one user prompt, or the results of
the tool calls the model just made (sometimes with a user note). Each tool call
and each tool result is its own message: a model turn with text and two tool
calls is three messages. The system prompt (Anthropic's `system`, Responses'
`instructions`) and tool catalogs aren't messages.

### The harness's messages

A message the user sends the model goes through a harness (Claude Code,
Codex), and the harness adds its own text to the conversation. That text is a
message of its own, `ROLE_SYSTEM`, never the user's:

- system and developer messages in the conversation: Claude Code's notes when
  tools are added or a file changes on disk, or a background task finishes;
  Codex's permissions;
- the notes the harness writes into a user's turn: a `<system-reminder>`, an
  interrupted request (`[Request interrupted by user…]`), Claude Code's own
  title and next-prompt suggestion requests (`<session>`,
  `[SUGGESTION MODE: …]`), Codex's `<environment_context>` and
  `<user_instructions>`, and another sender's mid-turn note ("The
  coordinator sent a message while you were working: …");
- the output the harness puts in a user's turn: Claude Code's bash-mode
  output (`<bash-stdout>`, `<bash-stderr>`; the command the user typed,
  `<bash-input>`, is theirs), a skill's body ("Base directory for this
  skill: …"), and a slash command's output and caveat
  (`<local-command-stdout>`, `<local-command-caveat>`; the command itself is
  the user's);
- Codex's `configuration_update` and `compaction_trigger` items (opaque:
  settings, not text).

A tool Claude Code adds mid-session (a `tool_addition` block in a system
message) is a `ToolAdded` part naming the tool; like the tool catalog, its
definition isn't sent, and a Replace returns it as it came. What the user
pastes (`<pasted_content>`) is the user's. A Codex `compaction` item (the
provider's encrypted summary of the conversation so far) carries no role:
it's an `Opaque` part with `ROLE_UNSPECIFIED`.

A user's turn is split accordingly: each note is its own `ROLE_SYSTEM`
message, the user's text a `ROLE_USER` one. Claude Code's image placeholders
ahead of the user's words (`[Image #2] what is this?`) label the image blocks
beside them: the `ROLE_USER` text is the words, and a Replace keeps the
placeholders; placeholders alone are a `ROLE_SYSTEM` note. When the user
types while Claude Code is working, Claude Code wraps the words ("The user sent a new message
while you were working: … This is how Claude Code surfaces messages…"): the
`ROLE_USER` text is just the user's words, and a Replace of them keeps the
wrapper on the wire. The harness's messages can be replaced, and answered
with a Respond, like any other.

**Nothing unknown makes a request unreadable.** A role, an item, a block or a
shape AIntercept doesn't know arrives as an `Opaque` part (present, never
changed; `ROLE_UNSPECIFIED` for an unknown role), and the rest of the request
is read. Only a body that isn't a conversation at all (not JSON, no
`messages` or `input`), or another endpoint on an enforced route (a token
count), goes upstream without `OnRequest`; the observer counts them by reason
(`/stats`: `interceptor.unparsed_reasons`). On a capture of 9,832 Claude Code
and Codex requests, one was: an empty `{}` body. (Before this, 5,450 Claude
Code requests, every one with a system message in its conversation, went
upstream unread.)

The **response** is split into messages as it streams: each text block (a
content block, a Responses message's text part, a Chat Completion's content),
each tool call, and each opaque part (reasoning). Every one reaches the
Interceptor before the client sees it. A reply is read by what its body is,
not only its headers: Codex's HTTP replies carry no `content-type`, so a
reply without one is held until its first bytes say whether it's an event
stream (an `event:` or `data:` line) or JSON. In observe mode a compressed
reply (gzip, br, zstd), which observe can't ask to be sent plain, is decoded
at its end and then read the same way; a request is read whole up to 32 MiB,
as in enforce.

## Callbacks

```proto
service Interceptor {
  rpc OnCallStart(CallStart) returns (CallVerdict);           // who, which client, which model, its options
  rpc OnTurnStart(TurnStart) returns (CallVerdict);           // a WebSocket's each request: its model and options
  rpc OnRequest(RequestMessages) returns (RequestVerdict);    // the call's new messages
  rpc OnReply(ResponseMessage) returns (MessageVerdict);      // the reply's text, piece by piece; opaque parts
  rpc OnToolCall(ResponseMessage) returns (MessageVerdict);   // each tool call the model makes, held whole
  rpc OnCallComplete(CallRecord) returns (Ack);               // the finished call: usage, timings
}
```

- **`OnCallStart`** fires once per call, before anything goes upstream. It
  carries `Call` (identity, client, host and path, `provider` and `api`;
  below, "Identity" and "Options") and `RequestInfo` (model, options, stream,
  declared tools, MCP tools, attachments, size), and can **Respond** or
  **Error**. It's content-free: no message is in it. For a WebSocket it fires
  at the upgrade, before any message (an Error there refuses the connection).
- **`OnTurnStart`** fires for each request on a WebSocket (Codex's
  `response.create`), before it goes upstream: `OnCallStart` for one turn.
  `TurnStart` carries the connection's `Call`, the turn's number
  (`ws_turn`, as on its `CallRecord`) and the turn's `RequestInfo`. Accept,
  **Respond** or **Error**. Content-free, so it's made with Interceptor
  content off too. In enforce, a WebSocket whose turns are decided has its
  client messages decoded and re-framed: AIntercept removes
  `Sec-WebSocket-Extensions` from the upgrade, so compression isn't
  negotiated. An Interceptor that doesn't implement it (UNIMPLEMENTED)
  accepts every turn, and AIntercept stops asking (and keeps compression).
- **`OnRequest`** fires once per call with the new messages, before they go
  upstream. Per message: **Accept**, or **Replace** it. For the call:
  **Respond**. Or **Fetch** parts first. A request that adds no message
  (Claude Code resuming a thread with an empty `messages` list) gets no
  `OnRequest`: there's nothing to decide. It still gets `OnCallStart` (which
  can Respond), and its reply goes to `OnReply` and `OnToolCall`; the
  observer counts these (`/stats`: `interceptor.no_new_messages`).
- **`OnToolCall`** fires for each tool call in the model's reply, once it is
  complete and before the client sees it: **Accept**, **Replace** (for
  example, change the arguments), or **Respond** (end the reply here with your
  text instead of the tool call). The message is the tool call alone: an
  `ASSISTANT` message whose id is the provider's call id, with one `ToolCall`
  part.
- **`OnReply`** fires for the reply's text as it streams, before the client
  sees any of it (below, "Reply text"): **Accept**, **Replace**, **Hold** or
  **Respond**, and `keep` to hold back only a tail. It also tells of each
  opaque part (reasoning, encrypted content), which can't be changed.
- **`OnCallComplete`** is asynchronous, in both modes: there's nothing left to
  decide. `CallRecord` carries the status, the model served, input, output,
  cache and reasoning tokens, stop reason, tool calls by name and count, time
  to first byte, duration, bytes, error, and the answers you gave (and whether
  they were applied). A WebSocket connection is one call: one `OnCallStart`
  at the upgrade, one `CallRecord` per model turn (`ws_turn` 1, 2, …) plus a
  final one when the connection closes (`ws_turn` 0).

`Call.call_id` is the same on every callback for a call, and is the
`correlation_id` on AIntercept's own records.

`OnRequest`, `OnReply` and `OnToolCall` need Interceptor content on. In
observe mode every callback is made the same way, queued off the stream;
answers are recorded, never applied. An Interceptor that doesn't implement
`OnTurnStart`, `OnReply` or `OnToolCall` (UNIMPLEMENTED) accepts what it
would have been asked, and AIntercept stops holding it: there's no cost for
what nobody handles.

### Answers

| Answer | Where | Effect |
|---|---|---|
| **Accept** | everywhere | unchanged (an empty verdict) |
| **Replace** | a request message, reply text, a tool call | AIntercept puts your version of the message in place, in the provider's format; everything else in the call is byte-for-byte as sent |
| **Hold** | reply text | nothing is released yet; the pending text comes back with what arrives next |
| `keep` | reply text, with Accept or Replace | the pending text's last `keep` bytes stay held; the rest goes (as it was, or replaced) |
| **Respond** | call start, a WebSocket turn, request, reply text, a tool call | the client gets your text as the model's reply (below) |
| **Error** | call start, a WebSocket turn, request | the client gets an error, written as the call's API writes its own; the call never goes upstream (below) |
| **Fetch** | request | AIntercept calls `OnRequest` again with those parts' content filled in |

There is no Drop. To refuse, answer with an Error the client already knows
how to handle, or answer for the model. Annotations (key/value) on any answer
are recorded with it.

### Respond

The client receives a normal reply, in the provider's own format (streamed if
it asked for a stream), containing your text, with zero usage.

How it's delivered depends on whether the conversation lives with the client:

- **The client keeps the conversation** (Anthropic Messages and OpenAI
  Responses without chaining, Chat Completions): AIntercept **synthesizes** the
  reply. The call never reaches the provider.
- **The provider keeps the conversation** (Claude Code's message threads,
  `previous_response_id`, ChatGPT and claude.ai on the web): a reply the
  provider never generated isn't in its history, so the next call, which
  points at it, would fail. AIntercept instead **rewrites the request** so the
  model replies with exactly your text. The call reaches the provider, and the
  conversation stays consistent.

`CallRecord.respond` says which was used, and so does the verdict row
(`action: respond`, `respond_mode: synthesized | rewritten`). A synthesized
call still gets its complete row: status 200, the synthesized reply, zero
usage.

As built:

- **Synthesized** replies: Anthropic's event sequence (`message_start`, one
  text block, `message_delta` with `stop_reason: end_turn`, `message_stop`)
  or its JSON message; the Responses events (`response.created` …
  `response.output_text.delta` … `response.completed`) over SSE or as Codex
  WebSocket messages, or the JSON response; Chat Completions chunks and
  `[DONE]`, or the JSON completion. Ids are fresh, in each provider's style
  (`msg_01…`, `resp_…`, `chatcmpl-…`).
- **Rewritten**: the newest new user message's last text part becomes "Reply
  with exactly this text and nothing else: " and your text (or, when the new
  messages are only tool results, the last result's text). Every other byte
  goes as sent. The model usually complies; it isn't guaranteed to.
- A Respond to a request AIntercept can't read, or at a WebSocket's
  `OnCallStart` (the connection's requests come later), is recorded and not
  applied.
- At a tool call, the call and the rest of the reply are replaced with your
  text, and the reply ends cleanly (`end_turn`, `response.completed` listing
  the items already sent and your message, or Chat's `finish_reason: stop`
  and `[DONE]`). What the provider sends after that is dropped.
- In reply text, the pending text and the rest of the reply are replaced with
  your text, in the same text block (what was released stays), and the reply
  ends the same way. The record keeps the provider's real usage: it
  generated the reply.

### Error

```proto
message Error {
  int32 http_status = 1;  // 400 to 599
  string type = 2;        // the API's error type; empty: its usual one for the status
  string message = 3;     // shown verbatim; empty: "Blocked by your organization's AI usage policy."
}
```

An Error refuses the call (`CallVerdict.error`, `RequestVerdict.error`) the
way the provider itself would, so the client fails in its own normal error
handling. AIntercept writes it as the call's API (`Call.api`) writes its
errors. Each format is checked against that API's real errors in the capture
corpus (`TestErrorShapesMatchCorpus`):

| API | Body | Headers | `type` when empty |
|---|---|---|---|
| Anthropic Messages | `{"type":"error","error":{"type":…,"message":…}}` | `x-should-retry: false` on a 4xx | by status: `invalid_request_error`, `authentication_error` (401), `permission_error` (403), `not_found_error` (404), `request_too_large` (413), `rate_limit_error` (429), `overloaded_error` (529), `api_error` (5xx) |
| OpenAI Responses, Chat Completions | `{"error":{"message":…,"type":…,"param":null,"code":null}}` | | `invalid_request_error`; `server_error` (5xx) |
| ChatGPT backend (Codex over HTTP) | `{"detail":…}` (what chatgpt.com sends; `type` isn't used) | | |
| Gemini | `{"error":{"code":…,"message":…,"status":…}}` (`type` is the status) | | `INVALID_ARGUMENT`; `UNAUTHENTICATED` (401), `PERMISSION_DENIED` (403), `NOT_FOUND` (404), `RESOURCE_EXHAUSTED` (429), `UNAVAILABLE` (503), `INTERNAL` (5xx) |
| A Codex WebSocket turn (`OnTurnStart`, or `OnRequest` on it) | the Responses API's error event, `{"type":"error","status":…,"error":{"type":…,"code":null,"message":…}}` | | as Responses |

Every response has `content-type: application/json`. A call outside these
APIs (a control-plane endpoint) gets its provider's format. On a WebSocket
turn the connection stays open: the error event ends the turn, and the next
one goes on. At a WebSocket's `OnCallStart`, the upgrade itself is refused.

**Per-person fast mode** is the motivating case. Anthropic's own control is
per organization, all or nothing: a fast request (`"speed": "fast"`) from an
organization without it gets a 400 with "Fast mode is not enabled for your
organization…", and Claude Code then retries the same turn at standard
speed. Answering a fast request with that same Error:

```go
Error{HttpStatus: 400, Type: "invalid_request_error", Message: "Fast mode is not enabled for your organization."}
```

downgrades the person, not blocks them: Claude Code falls back to standard
speed exactly as it does on Anthropic's own refusal, and tells them.

Enforce only: in observe mode an Error is recorded (`action: refuse`,
`applied: false`) and the call goes on. An Error whose status isn't 400 to
599, or an OnRequest answer with both an Error and a Respond, is an invalid
answer (`onError`). An Error isn't an answer to the model's reply (`OnReply`,
`OnToolCall`): end that with a Respond.

### Reply text

No reply text reaches the client until `OnReply` has decided it. Per text
block, each call carries:

- `released`: the block's text already released to the client. It's never
  asked about again.
- the message's one `Text` part: the **pending** text, the pieces that came
  since the last answer plus anything held back.
- `done`: the block is finished; nothing more of it will come.

The message id is the block's, the same on every call for it
(`<the reply's id>:<the block's index>`: `msg_…:0`, a Responses message's
`msg_…:0`, `chatcmpl-…:0`). The answers:

- **Accept**: the pending text goes out exactly as received (the provider's
  own events, byte for byte).
- **Replace** with the message, its text changed: your text goes out instead,
  as one text event in the stream's own format. For the Responses API, every
  later event and item that repeats the text (`output_text.done`,
  `content_part.done`, `output_item.done`, `response.completed`) says what the
  client got.
- **Hold**: nothing goes out; the pending text comes back, with what arrives
  next, in the next call. This is how a match spanning pieces is seen whole.
- `keep` (with Accept or Replace): the last `keep` bytes of the pending text
  (cut on a character) stay held for the next call; the rest goes out, as it
  was or replaced. A rule watching for a phrase holds back only a tail that
  could still become it, and the text before it isn't delayed.
- **Respond**: above.

At the block's end (`done`), nothing can stay held: a Hold releases the text
as Accept, and `keep` is ignored. Events that aren't text (a ping, the
block's stop) wait behind held text, in order.

**One call at a time, and latency doesn't compound.** While a call is
outstanding, the pieces that arrive gather for the next one: AIntercept reads
Envoy's messages on their own goroutine and asks about the pending text only
when nothing more of the reply is waiting. A burst of pieces is one call. Each
piece of text waits for one `OnReply` round trip: measured through the
enforcing filter with a package `intercept` Interceptor over gRPC on loopback,
about 60 µs per streamed event (66–73 µs against 12 µs when text isn't held;
`BenchmarkReplyPiece`); in a cluster, add the network round trip to the
Interceptor. Through a real Envoy, the end-to-end test sees no added delay
beyond its fixture's timer jitter (`TestV2ReplyLatency`).

**Why a new callback, not a general one for every response message.** One
callback for text and tool calls was the alternative. A separate `OnReply` is
the simpler change: it's additive (an Interceptor written for `OnToolCall`
keeps working unchanged), the two shapes differ (text is a stream of pieces
with `released`, `done`, Hold and `keep`; a tool call is one whole message),
and leaving `OnReply` unimplemented is how an Interceptor that has no rules
on reply text tells AIntercept not to hold it, the same way `OnToolCall`
already worked. Both take `ResponseMessage` and answer `MessageVerdict`.

**Opaque parts** (an Anthropic `thinking` or `redacted_thinking` block, a
Responses `reasoning` item) go to `OnReply` once each, as a message with one
`Opaque` part and `done` set, in the reply's order. They can't be read or
changed: they go out as they came, and the answer is ignored.

A non-streamed JSON reply is held whole: each text block (as done, `released`
empty) and each tool call is decided in order before the reply is released.

### Tool calls held whole

Each tool call is held from its first event (`content_block_start` of a
`tool_use` block; `response.output_item.added` of a `function_call` or
`custom_tool_call`; Chat's first `tool_calls` delta for an index) until it's
complete (`content_block_stop`; `response.output_item.done`; the next tool
call's index, or the finish), then `OnToolCall` decides it:

- **Accept**: the held events go out exactly as received.
- **Replace**: the call goes out with the new arguments, in the stream's own
  events (one argument delta with all of them, and the `.done` events and the
  final `response.completed` rewritten to match).
- **Respond**: as above.

The client gets each call when it's complete plus one `OnToolCall` round
trip, rather than piece by piece. A refusal at a tool call or in reply text
(the Interceptor failed, was late or gave an answer that can't be applied,
with `onError: deny`) ends the reply with the provider's error event (HTTP)
or closes the WebSocket; with `onError: allow` the held text or call goes out
as it came. A tool call over 32 MiB, or a text block over the hold cap, goes
out undecided; a text block whose released and pending text pass 3 MiB is
released undecided from then on.

### What's recorded

Every answer other than a plain Accept is a verdict row (`callback`:
`call_start`, `turn_start`, `request`, `reply` or `tool_call`; `action`:
`accept` with annotations, `replace`, `hold`, `respond`, `refuse` (an Error,
its `reason` the status, type and message the client got) or `error` (a
failure)), with the message's id (a WebSocket turn's number, for
`turn_start`), in both modes (`applied: false` in observe). `CallRecord.verdicts` carries the
call's rows: an HTTP call's, or on a WebSocket, each turn's own. Each row is
also stored in Postgres (`verdicts`, keyed by the call and the row's sequence
number; at most 1000 per call), written with the call.

The rows are in AIntercept's raw stream with phase `verdict`, keyed by the
call's `correlation_id`, with `callback`, `action`, `applied`, `mode`,
`reason`, `annotations`, `latency_ms`, `message_id` and, for an applied
Respond, `respond_mode`. Every failure is a row too (`action: error`). A call
refused because the Interceptor failed carries the status it was refused with
on its complete row, and `error: "denied by interceptor: …"`; one refused
with an Error, `error: "refused by interceptor: <message>"`.

The observer's `/stats` has an `interceptor` block: queued, dropped,
delivered, failed, decided, decide_failed, unparsed (and its reasons),
no_new_messages and held_for_identity.

### What an Interceptor can't see or change

- Opaque parts: it's told they exist, never what's in them, and can't change
  them.
- Bodies that aren't a conversation (not JSON, no messages), other endpoints
  on an enforced route (a token count), and bodies over the 32 MiB enforce
  cap: they go upstream untouched, counted by reason, without `OnRequest`.
- The system prompt and tool catalogs aren't messages.
- What a harness's note quotes is the harness's: a `<system-reminder>`
  quoting a file the user wrote is `ROLE_SYSTEM`, not the user's prompt. An
  unknown tag in a user's turn (a local hook's, say) stays the user's text.
- In enforce, responses with a `content-encoding` AIntercept didn't remove
  (it asks for them plain), and WebSocket connections where the server
  negotiated compression the client was never offered: they pass unevaluated
  (counted). In observe, a compressed or JSON reply over 32 MiB (decoded) is
  counted (`reply over the buffer`), not read.
- Only the first choice of a Chat Completion with several is decided.
- ChatGPT and claude.ai on the web aren't parsed yet.

### Fetch

`Fetch{part_ids}` makes AIntercept call the same callback again for the same
messages, with those parts' content inline (one Fetch per call). A fetched part
over 32 MiB isn't available (its size and preview are all there is). Fetch costs
one more round trip, inside the same deadline. A fetched part can be larger than
gRPC's default 4 MiB, so an Interceptor that fetches raises its receive limit to
33 MiB.

## Options

A call's provider settings (how hard the model reasons, fast mode, the
priority tier) are one list, keyed by an enum, on the request
(`RequestInfo.options`: what the client asked for) and on the response
(`ResponseInfo.options`: what the provider reports serving it with). The same
keys on both sides, so "asked for fast, served standard" is one key:

```proto
enum Option {
  OPTION_UNSPECIFIED = 0;
  OPTION_EFFORT = 1;        // how hard the model reasons
  OPTION_SPEED = 2;         // Anthropic's fast mode ("fast"); absent: standard
  OPTION_SERVICE_TIER = 3;  // the priority tier
  OPTION_THINKING = 4;      // Anthropic's thinking mode
}
message OptionValue { Option key = 1; string value = 2; }
```

- **AIntercept reads them.** Each from where the call's API puts it (below),
  only these keys, and only a short string value (64 bytes at most): no text
  reaches a content-free callback this way. An Interceptor never parses a
  provider's body.
- **Values are as the provider sends them.** `Call.api` says how to read
  them (OpenAI's `priority` tier and Anthropic's are different products).
- **Absent means the request didn't set it.** A standard-speed request has no
  `OPTION_SPEED`: Claude Code sends `speed` only when fast mode is on. Its
  `anthropic-beta: fast-mode-…` header says only that the client *can* use
  fast mode, and comes on standard requests too: never key a rule on it.
- **A new setting is a new `Option` value, never a new field.** An older
  Interceptor that gets a key it doesn't know still decodes it (proto3 keeps
  unknown enum values). Mapping an option for an API is on
  [`PROVIDERS.md`](PROVIDERS.md)'s checklist.

Values are as each API sends them: the ones listed were seen in the capture
corpus; those marked * are the provider's own, with no capture yet
(`KNOWN_GAPS` lists every option no capture proves).

| Option | API | Request (`RequestInfo.options`) | Response (`ResponseInfo.options`) | Values |
|---|---|---|---|---|
| `OPTION_EFFORT` | Anthropic Messages | `output_config.effort` | | `low`, `medium`, `high`, `xhigh` |
| | ChatGPT backend (Codex, HTTP and WebSocket), OpenAI Responses | `reasoning.effort` | `reasoning.effort` (echoed) | `low`, `medium`, `xhigh` |
| | Chat Completions | `reasoning_effort` | | as Responses* |
| | Gemini | `generationConfig.thinkingConfig.thinkingLevel` | | `low`, `high`* |
| `OPTION_SPEED` | Anthropic Messages | `speed` | `usage.speed` | request: `fast`* (absent: standard); response: `standard`, `fast`* |
| `OPTION_SERVICE_TIER` | Anthropic Messages | `service_tier` | `usage.service_tier` | request: `auto`, `standard_only`*; response: `standard`, `priority`*, `batch`* |
| | ChatGPT backend, OpenAI Responses, Chat Completions | `service_tier` | `service_tier` (the tier that served it) | `auto`; `default`, `flex`, `priority`* |
| `OPTION_THINKING` | Anthropic Messages | `thinking.type` | | `adaptive`; `enabled`, `disabled`* |

`Call.provider` is who serves the call (the company: a coarse policy, "no
OpenAI at all"), and `Call.api` which of its APIs it's on (the wire format:
where an option came from, what its values mean, and how an Error is
written):

| `Call.api` | Host and path | `Call.provider` |
|---|---|---|
| `API_ANTHROPIC_MESSAGES` | api.anthropic.com `/v1/messages` (an API key or OAuth) | `PROVIDER_ANTHROPIC` |
| `API_CLAUDE_AI_WEB` | claude.ai | `PROVIDER_ANTHROPIC` |
| `API_OPENAI_RESPONSES` | api.openai.com `/v1/responses` | `PROVIDER_OPENAI` |
| `API_OPENAI_CHAT_COMPLETIONS` | api.openai.com `/v1/chat/completions` | `PROVIDER_OPENAI` |
| `API_CHATGPT_BACKEND` | chatgpt.com `/backend-api` (Codex, ChatGPT on the web) | `PROVIDER_OPENAI` |
| `API_GEMINI` | generativelanguage.googleapis.com | `PROVIDER_GOOGLE` |
| `API_UNSPECIFIED` | any other path: a control-plane endpoint (an account lookup, analytics) | by host |

The client software (`Call.client.product`) is a third axis.

**Removed:** `RequestInfo.effort` (field 2) is `OPTION_EFFORT` now; its name
and number are reserved. It used to fall back to Anthropic's `thinking.type`
when a request set no effort; that's `OPTION_THINKING` now. Code that reads
`effort` stops compiling once you regenerate; an older Interceptor that isn't
regenerated reads it as empty on every call, so a rule on it stops matching
without an error.

**Upgrading:** upgrade the observers before an Interceptor answers with an
Error. An older observer doesn't know the answer and accepts the call: the
refusal is silently not applied.

## Identity

`Call.identity` says who is calling:
- the salted key hash and its display hint;
- the client IP;
- the client's device and session IDs (Claude Code's `metadata.user_id`,
  Codex's turn metadata);
- the person's `email` and `name`, and `email_source`, how they were
  established;
- `reported_account_id`, the account the client says it is (the body's
  `metadata.user_id`, the `chatgpt-account-id` header, the token's claim);
- `verified_account_id`, the account the provider confirmed for this call's
  credential, and `account_mismatch` when the client reported another one;
- `org_id`: the organization the provider confirmed, else the one the request
  names.

**Removed:** `account_id` (field 4) was removed in the release after v0.6.2;
its name and number are reserved. It had been empty since v0.6.2. Use
`verified_account_id`, or `reported_account_id` for what the client says.
Code that reads it stops compiling once you regenerate from the new proto.

The first time an observer pod sees a credential, it asks the provider's own
account endpoint who owns it, once, with the caller's token, and keeps the
answer for 24 hours (`docs/PROVIDERS.md`, "Identity", lists each provider's
lookup). The lookup starts at the request's headers, so it's usually done
before `OnCallStart`. In observe mode the client never waits for it: the
call's callbacks wait, up to `observer.identityLookup.maxWaitMs` (300 ms),
in a buffer of the call's own. In enforce mode the call's first request waits
up to `enforceMaxWaitMs` (300 ms; 0: never). Every later call with the
credential is a map read. A call's identity is resolved once, so every
callback of the call carries the same one.

| `email_source` | Where `email` and `name` come from | How to use it |
|---|---|---|
| `CREDENTIAL` | The provider confirmed them for this exact credential (its lookup, or its own account response to a call with the credential). | **Grant privileges on this one only.** What the client sends never overrides it. |
| `CLAIMS` | The bearer token's own claims (a JWT), not verified. | Only as strong as the provider's later check of the token: you decide before the provider sees the call. |
| `ACCOUNT` | An account seen before under the id the client reports. | A hint: the client can send another person's id. Apply your default policy. |
| `UNSPECIFIED` | Nothing known. | Default policy. |

## Modes

|  | Observe | Enforce |
|---|---|---|
| Which traffic | Every intercepted call, by default | The hosts and paths listed in `interceptor.enforce` |
| `OnCallStart`, `OnRequest`, `OnReply`, `OnToolCall` | Queued and delivered asynchronously; traffic never waits | In the call's path: Envoy waits for the answer |
| Answers | Recorded as what would have happened (`applied: false`) | Applied (`applied: true`) |
| `OnCallComplete` | Async | Async |
| Interceptor slow or down | Callbacks are delayed, then dropped and counted (`/stats`). Traffic is unaffected. | After `deadlineMs` (default 50 ms), or on error: the call is refused, 503 "interceptor unavailable" (`onError: deny`, the default), or let through (`onError: allow`) |
| Observer down | Traffic is unaffected | Enforced calls are refused (Envoy fails closed) |

`Call.mode` tells your service which mode a callback is in.

**The rollout path:**
1. Run your Interceptor in observe mode against all traffic.
2. Read the recorded answers to see what it would have done.
3. Move specific hosts and paths to enforce.

Your code doesn't change between the steps.

## Interceptor content (separate from what AIntercept records)

`interceptor.content` decides whether `OnRequest`, `OnReply` and `OnToolCall`
exist. It's independent of `interceptionMode`, which decides what AIntercept
itself records:

| | `interceptionMode: content-free` | `interceptionMode: full` |
|---|---|---|
| `interceptor.content: false` | Neither stores nor sends content | Records bodies; the Interceptor sees metadata only |
| `interceptor.content: true` | **The Interceptor sees the messages, AIntercept stores none** (e.g. redaction without retention) | Both |

Without content, an Interceptor can still decide on everything in
`CallStart` and `TurnStart`: model, options, tools, client, identity,
provider, API, host and path (Respond or Error).

With content on, enforce asks upstream for replies it can read and release
without re-compressing: it removes `accept-encoding` from a request whose
reply goes to `OnReply` or `OnToolCall`, and `Sec-WebSocket-Extensions` from
WebSocket upgrades, so WebSocket compression isn't negotiated.

## Watches

A phrase in reply text can span pieces. Package `intercept`'s watches find it
over this protocol: each call holds back (`keep`) only the tail that could
still become a match, so the match is seen whole while the text before it
goes on ([`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md)).

## Deploying

Helm values:

```yaml
interceptor:
  endpoint: my-interceptor.policy.svc:9100   # host:port of your service
  inlineLimitBytes: 65536                    # larger parts go by reference
  content: true                              # OnRequest, OnReply, OnToolCall
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
  # or, instead of tokenSecret (see Authentication below):
  # serviceAccountToken: { enabled: true, audience: my-interceptor }
```

- **Without the chart:** set the observer's environment: `INTERCEPTOR_ENDPOINT`,
  `INTERCEPTOR_INLINE_LIMIT_BYTES`, `INTERCEPTOR_CONTENT`,
  `INTERCEPTOR_DEADLINE_MS`, `INTERCEPTOR_ON_ERROR`, `INTERCEPTOR_TLS`,
  `INTERCEPTOR_TLS_CA_FILE`, `INTERCEPTOR_TLS_CERT_FILE`,
  `INTERCEPTOR_TLS_KEY_FILE`, `INTERCEPTOR_TLS_SERVER_NAME`, `INTERCEPTOR_TOKEN`
  or `INTERCEPTOR_TOKEN_FILE`, plus `INTERCEPTOR_OBSERVE_TIMEOUT_MS`, `INTERCEPTOR_QUEUE_SIZE` and
  `INTERCEPTOR_WORKERS` for observe delivery. Identity (`observer.identityLookup`,
  `identityCache`, `identityDirectory` in the chart): `OBSERVER_IDENTITY_LOOKUP`
  (`false` turns lookups off), `OBSERVER_IDENTITY_LOOKUP_PROVIDERS`,
  `OBSERVER_IDENTITY_MAX_WAIT_MS`, `OBSERVER_IDENTITY_ENFORCE_MAX_WAIT_MS`,
  `OBSERVER_IDENTITY_LOOKUP_TIMEOUT_MS`, `OBSERVER_IDENTITY_LOOKUP_MAX_IN_FLIGHT`,
  `OBSERVER_IDENTITY_LOOKUP_RATE_PER_SECOND`, `OBSERVER_IDENTITY_LOOKUP_BURST`,
  `OBSERVER_IDENTITY_CACHE_MAX_ENTRIES`, `OBSERVER_IDENTITY_CACHE_TTL`,
  `OBSERVER_IDENTITY_CACHE_NEGATIVE_TTL`, `OBSERVER_IDENTITY_CACHE_ERROR_TTL`,
  `OBSERVER_IDENTITY_ANTHROPIC_ADMIN_KEY` and `OBSERVER_IDENTITY_DIRECTORY_INTERVAL`.
- **Enforce scopes need an Envoy route:** the hosts in `interceptor.enforce`
  must be in `interception.allowlist`, and the chart checks this.
- **Where enforce scopes take effect:** they're applied by the Helm chart's
  Envoy config. The local compose stack is observe only;
  `scripts/test-interceptor.sh` runs both modes.

### Authentication

Every callback carries `authorization: Bearer <token>`, from one of these
(or neither):

| | `interceptor.tokenSecret` | `interceptor.serviceAccountToken` |
|---|---|---|
| The token | a static value from a Secret's `token` key | the observer's Kubernetes service-account token, projected for `audience` |
| Rotation | by hand, on both sides | the kubelet renews it (at 80% of `expirationSeconds`, default 600); the observer re-reads the file at least every 30 s |
| Your Interceptor checks | it equals the shared value | it's a JWT signed by the cluster's OIDC issuer: verify against the issuer's JWKS, and check `iss`, `aud` (your audience), `sub` (`system:serviceaccount:<namespace>:<observer ServiceAccount>`) and `exp` |
| Secret to share | yes | none |

For a service-account token: cache the JWKS and refresh it in the
background, re-fetching once on an unknown `kid` (the issuer rotates keys
without notice); allow a little clock skew; don't reject unknown claims (a
projected token also names the pod and node); and don't pin the
ServiceAccount's `uid`, which changes if it's recreated. Setting both
`tokenSecret` and `serviceAccountToken` is an error. Mutual TLS
(`tls.clientCert`) can be added on top of either.

**Running enforce:** an enforced call depends on the observer and on your
Interceptor. Run both highly available:
- at least 3 ingest pods (each runs Envoy and an observer). With enforce
  scopes set, the chart requires at least 2 (`envoy.autoscaling.minReplicas`)
  and adds a PodDisruptionBudget;
- your service on the same footing, close to the observer (same region, same
  cluster ideally);
- latency in mind: with content on, each piece of reply text and each tool
  call waits for one round trip.

Start with narrow scopes (one path), and widen them once the answers recorded
in observe mode look right.

## The examples

- **`aintercept/examples/rabbit`** (Go, package `intercept`) is the smallest:
  two rules that answer two phrases in the user's prompts with Reply. Set
  `INTERCEPT_FAST_MODE=refuse` and it also refuses Anthropic's fast mode with
  an Error (a 400 saying `INTERCEPT_FAST_MODE_MESSAGE`, or a default), except
  for the verified emails in `INTERCEPT_FAST_MODE_ALLOW` (comma-separated).
- **`aintercept/examples/redact`** (Go, package `intercept`) replaces "silly
  clown" with `[redacted]` in every message both ways (prompts, tool results
  and the harness's notes to the model; the reply's text, even split across
  streamed pieces, and its tool calls back), answers a call for the model
  `forbidden-model` with a refusal at `OnCallStart`, and logs who makes each
  call and how it ended.
- **`aintercept/examples/python-redact`** does the same on the raw protocol,
  in Python, for prompts, tool results and reply text (it has no
  `OnToolCall`): its `OnReply` holds back a tail that could still become the
  phrase (`keep`).

rabbit and redact are built into the `aintercept` image as
`/rabbit-interceptor` and `/redact-interceptor`:

```bash
# Next to an observer: listens on :9100
docker run --entrypoint /redact-interceptor <aintercept image>
```

`make test-interceptor` runs redact through a real Envoy (prompts and replies
redacted in enforce, untouched in observe, `forbidden-model` answered with
the refusal, enforced calls refused with a 503 and observed ones carrying on
when it's stopped), and the suite's own Interceptor (the rabbit's rules plus
rules on the reply and its tool calls, and options: a fast request refused
with Anthropic's own 400, a Codex turn asking for the priority tier refused
with its error event) in every format.

## Writing one in another language

Generate the stubs from the proto with your language's gRPC toolchain, for
example:

```bash
python -m grpc_tools.protoc -I aintercept --python_out=. --grpc_python_out=. aintercept/interceptor/v2/interceptor.proto
```

The service is `aintercept.interceptor.v2.Interceptor`. Implement the
callbacks you need (an unimplemented `OnTurnStart`, `OnReply` or
`OnToolCall` accepts everything), and return quickly from all but `OnCallComplete`: in enforce
mode they're in the call's path. Messages stay under gRPC's default 4 MiB
receive limit unless you Fetch; an Interceptor that fetches raises it to
33 MiB. For Go, import `…/aintercept/interceptor/v2` directly, or use package
`intercept`. Regenerate the Go code after changing the proto with
`make proto`.

## Test matrix

Each row runs per client: Claude Code, Codex over its WebSocket, Codex over
HTTP, ChatGPT on the web, claude.ai. The first cut covers Claude Code and
Codex; the web apps follow.

| # | Test |
|---|---|
| **What's new** | |
| N1 | A new prompt: only it arrives, not the history |
| N2 | Tool results after a tool call: only they arrive |
| N3 | A chained request (message threads, `previous_response_id`): every item arrives |
| N4 | The same prompt from each client arrives as the same `ConvMessage` |
| N5 | An unknown request format: passes through untouched and is counted, never broken |
| N6 | The harness's messages (system messages in the conversation, notes in a user's turn) arrive as ROLE_SYSTEM; the user's mid-turn words as their prompt |
| N7 | An unknown role, item, block or shape inside a conversation arrives as Opaque; the rest is read |
| N8 | A request with no new messages gets OnCallStart, no OnRequest; its reply's callbacks as any |
| N9 | A Codex HTTP reply (no content-type) reaches OnReply and OnToolCall, in both modes; a compressed reply in observe; a request over 4 MiB in observe |
| **References** | |
| R1 | A part over the inline limit arrives as a reference with size and preview |
| R2 | Fetch returns its content; a second round trip, inside the deadline |
| R3 | An image, a file, a large tool result, each by reference |
| **Replace** | |
| P1 | A replaced prompt reaches the provider in place; history and every other byte unchanged |
| P2 | A replaced tool result |
| P3 | A replaced tool call's arguments reach the client; the agent runs the new ones |
| P4 | Mixed answers on one call's messages apply correctly |
| **Respond** | |
| S1 | Synthesized: the client shows the text as a normal reply, streamed and not |
| S2 | Synthesized: the next turn works (the reply is in the client's history) |
| S3 | Rewritten (provider keeps the conversation): the model replies with the text; the next turn works |
| S4 | Respond on a tool call ends the reply cleanly; the agent stops |
| S5 | A responded call is recorded with zero usage, marked synthesized or rewritten |
| **Options and Error** | |
| O1 | Every option on every API that carries it arrives in `OnCallStart` (and a WebSocket turn's in `OnTurnStart`), and the response's in `OnCallComplete` |
| O2 | A standard request carrying the fast-mode beta header has no `OPTION_SPEED` |
| O3 | An Error at call start or on a request reaches the client in its API's format and headers; the call never goes upstream |
| O4 | An Error on a WebSocket turn: the API's error event, the connection stays open, the next turn goes on |
| O5 | Claude Code refused fast mode with Anthropic's 400 retries at standard speed (Anthropic's documented behavior; needs a real Claude Code with fast mode, not automated) |
| O6 | Observe: an Error is recorded, not applied |
| **Streaming** | |
| T1 | Reply text is decided piece by piece before the client sees it: Accept byte for byte, Replace, Hold, keep, Respond, in every format |
| T2 | Tool calls are held until complete, then released (or replaced), Chat Completions too |
| T3 | A phrase split across two and three pieces is replaced; nothing of it reaches the client |
| T4 | Pieces that arrive while a call is outstanding gather into the next |
| **Everywhere** | |
| E1 | Observe mode: every answer recorded, none applied |
| E2 | The Interceptor down or late: `onError` (allow passes through) |
| E3 | Only reasoning and encrypted parts: present as Opaque, never changed |
| E4 | Side calls (titles, token counts, quota checks) aren't broken |
| E5 | Messages stay under the gRPC default 4 MiB, whatever the conversation holds (Fetch replies excepted) |

## Next

- ChatGPT and claude.ai on the web.
- The same "what's new" parsing then serves storage: recording each message
  once, instead of every call's full history.
