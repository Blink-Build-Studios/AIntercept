# Interceptor v2: one message at a time

Status: **built for Claude Code and Codex** (Anthropic Messages, OpenAI
Responses over HTTP and Codex's WebSocket, Chat Completions): every message
in both directions, before it goes on. The request's new messages, Replace,
Fetch and Respond; the reply's text piece by piece and its tool calls whole,
each decided before the client sees it. Not yet: ChatGPT and claude.ai on
the web. v1 ([`INTERCEPTOR.md`](INTERCEPTOR.md)) stays as it is, and both run
side by side while integrations move over.

Most Interceptors are written with the Go library, package `intercept`
(rules, hooks and watches over this protocol):
[`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md). This
document is the protocol.

## Why a v2

v1 hands an Interceptor the wire: the whole request body, then each streamed
event. Three things follow from that, and all three are wrong for an
integrator:

- **Every call resends the conversation.** Claude Code and Codex send the whole
  history on each call, so an Interceptor sees (and must re-check) every
  earlier message again, and a long session's body is several MiB per call.
  A rule on "what the user said" fires again on every later call unless the
  Interceptor works out which message is new, per provider.
- **The integrator has to know every provider's format.** Anthropic Messages,
  OpenAI Responses (over HTTP and Codex's WebSocket), ChatGPT's and claude.ai's
  web formats, each with its own streaming events.
- **Streamed text arrives in pieces.** A phrase can span two events.

v2 moves all of that into AIntercept. An Interceptor sees **only the new
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
  rpc OnCallStart(CallStart) returns (CallVerdict);           // who, which client, which model
  rpc OnRequest(RequestMessages) returns (RequestVerdict);    // the call's new messages
  rpc OnReply(ResponseMessage) returns (MessageVerdict);      // the reply's text, piece by piece; opaque parts
  rpc OnToolCall(ResponseMessage) returns (MessageVerdict);   // each tool call the model makes, held whole
  rpc OnCallComplete(CallRecord) returns (Ack);               // the finished call: usage, timings
}
```

- **`OnCallStart`** fires once per call, before anything goes upstream. It
  carries `Call` (as in v1: identity, client, model; `docs/INTERCEPTOR.md`,
  Identity, for `email_source` and the verified account) and can **Respond**.
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
- **`OnCallComplete`** is asynchronous, as in v1: usage, timings, the verdicts
  and whether they were applied.

`OnRequest`, `OnReply` and `OnToolCall` need Interceptor content on. In
observe mode every callback is made the same way, queued off the stream;
answers are recorded, never applied. An Interceptor that doesn't implement
`OnReply` or `OnToolCall` (UNIMPLEMENTED) accepts what it would have been
asked, and AIntercept stops holding it: there's no cost for what nobody
handles.

### Answers

| Answer | Where | Effect |
|---|---|---|
| **Accept** | everywhere | unchanged (an empty verdict) |
| **Replace** | a request message, reply text, a tool call | AIntercept puts your version of the message in place, in the provider's format; everything else in the call is byte-for-byte as sent |
| **Hold** | reply text | nothing is released yet; the pending text comes back with what arrives next |
| `keep` | reply text, with Accept or Replace | the pending text's last `keep` bytes stay held; the rest goes (as it was, or replaced) |
| **Respond** | call start, request, reply text, a tool call | the client gets your text as the model's reply (below) |
| **Fetch** | request | AIntercept calls `OnRequest` again with those parts' content filled in |

There is no Deny or Drop.

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
`call_start`, `request`, `reply` or `tool_call`; `action`: `accept` with
annotations, `replace`, `hold`, `respond` or `error`), with the message's id,
in both modes (`applied: false` in observe). `CallRecord.verdicts` carries the
call's rows: an HTTP call's, or on a WebSocket, each turn's own. Each row is
also stored in Postgres (`verdicts`, keyed by the call and the row's sequence
number; at most 1000 per call), written with the call.

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

## Watches

A phrase in reply text can span pieces. Package `intercept`'s watches find it
over this protocol: each call holds back (`keep`) only the tail that could
still become a match, so the match is seen whole while the text before it
goes on ([`BUILDING_AN_INTERCEPTOR_V2.md`](BUILDING_AN_INTERCEPTOR_V2.md)).

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

## Rollout

- `interceptor.contract: v1 | v2` in the chart; v1 is the default until v2 has
  passed the matrix for the clients in use.
- The example (`aintercept/examples/rabbit`, on package `intercept`) answers
  two phrases in the user's prompts with Reply.
- The same "what's new" parsing then serves storage: recording each message
  once, instead of every call's full history.
