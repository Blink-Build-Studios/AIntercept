# Interceptor v2: one message at a time

Status: **built for Claude Code and Codex** (Anthropic Messages, OpenAI
Responses over HTTP and Codex's WebSocket; Chat Completions requests): the
new messages, Replace, Fetch, Respond and tool calls held whole. Not yet:
watches on streamed text, ChatGPT and claude.ai on the web, and holding Chat
Completions tool calls. v1 ([`INTERCEPTOR.md`](INTERCEPTOR.md)) stays as it
is, and both run side by side while integrations move over.

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
  Role role = 2;              // USER, ASSISTANT, TOOL
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
calls is three messages. System prompts, developer instructions and tool
catalogs aren't messages. Content of a type AIntercept doesn't recognise
arrives as Opaque.

The **response** is split into messages as it streams: the reply text, and
each tool call.

## Callbacks

```proto
service Interceptor {
  rpc OnCallStart(CallStart) returns (CallVerdict);           // who, which client, which model
  rpc OnRequest(RequestMessages) returns (RequestVerdict);    // the call's new messages
  rpc OnToolCall(ResponseMessage) returns (MessageVerdict);   // each tool call the model makes, held whole
  rpc OnCallComplete(CallRecord) returns (Ack);               // the finished call: usage, timings
}
```

- **`OnCallStart`** fires once per call, before anything goes upstream. It
  carries `Call` (as in v1: identity, client, model) and can **Respond**.
- **`OnRequest`** fires once per call with the new messages, before they go
  upstream. Per message: **Accept**, or **Replace** it. For the call:
  **Respond**. Or **Fetch** parts first.
- **`OnToolCall`** fires for each tool call in the model's reply, once it is
  complete and before the client sees it: **Accept**, **Replace** (for
  example, change the arguments), or **Respond** (end the reply here with your
  text instead of the tool call). The message is the tool call alone: an
  `ASSISTANT` message whose id is the provider's call id, with one `ToolCall`
  part. Enforce only.
- **Reply text streams.** It reaches the client as it's generated; v2 doesn't
  hold it. Rules on reply text come with watches (below, next).
- **`OnCallComplete`** is asynchronous, as in v1: usage, timings, the verdicts
  and whether they were applied.

### Answers

| Answer | Where | Effect |
|---|---|---|
| **Accept** | everywhere | unchanged (an empty verdict) |
| **Replace** | a request message, a tool call | AIntercept puts your version of the message in place, in the provider's format; everything else in the call is byte-for-byte as sent |
| **Respond** | call start, request, a tool call | the client gets your text as the model's reply (below) |
| **Fetch** | request, tool call | AIntercept calls the same callback again with those parts' content filled in |

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
  text, and the reply ends cleanly (`end_turn`, or `response.completed`
  listing the items already sent and your message). What the provider sends
  after that is dropped.

### Tool calls held whole

A reply's text reaches the client as it streams. Each tool call is held from
its first event (`content_block_start` of a `tool_use` block;
`response.output_item.added` of a `function_call` or `custom_tool_call`)
until it's complete (`content_block_stop`; `response.output_item.done`), then
`OnToolCall` decides it:

- **Accept**: the held events go out exactly as received.
- **Replace**: the call goes out with the new arguments, in the stream's own
  events (one argument delta with all of them, and the `.done` events and the
  final `response.completed` rewritten to match).
- **Respond**: as above.

A non-streamed JSON reply is held whole and its tool calls decided in order
before it's released. The cost is latency on tool calls only: the client gets
each call when it's complete plus one `OnToolCall` round trip, rather than
piece by piece; the text before it isn't delayed. A refusal (the Interceptor
failed or was late, `onError: deny`) ends the reply with the provider's error
event (HTTP) or closes the WebSocket. A tool call over 32 MiB goes out
undecided. An Interceptor that doesn't implement `OnToolCall` accepts every
call, and AIntercept stops holding them.

### Fetch

`Fetch{part_ids}` makes AIntercept call the same callback again for the same
messages, with those parts' content inline (one Fetch per call). A fetched part
over 32 MiB isn't available (its size and preview are all there is). Fetch costs
one more round trip, inside the same deadline. A fetched part can be larger than
gRPC's default 4 MiB, so an Interceptor that fetches raises its receive limit to
33 MiB.

## Next: watches on streamed text

Reply text streams, and a phrase can span two pieces of it. Watches will let an
Interceptor declare, at `OnCallStart`, literal strings or bounded regexes to
look for in the reply text (and in tool arguments and request text).
AIntercept matches over the decoded text, holding back only as many characters
as the longest pattern, and calls `OnMatch` only on a match: Accept, Replace
the matched text, or Respond (end the reply there). A call with no matches
costs no round trips after the request. Specified separately, after v2's first
cut.

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
| T1 | Reply text streams without waiting on the Interceptor |
| T2 | Tool calls are held until complete, then released (or replaced) |
| **Everywhere** | |
| E1 | Observe mode: every answer recorded, none applied |
| E2 | The Interceptor down or late: `onError` (allow passes through) |
| E3 | Only reasoning and encrypted parts: present as Opaque, never changed |
| E4 | Side calls (titles, token counts, quota checks) aren't broken |
| E5 | Messages stay under the gRPC default 4 MiB, whatever the conversation holds (Fetch replies excepted) |

## Rollout

- `interceptor.contract: v1 | v2` in the chart; v1 is the default until v2 has
  passed the matrix for the clients in use.
- The examples (Go and Python) move to v2 with the same rules: "I'm a silly
  rabbit" and "first we bing", as Respond.
- The same "what's new" parsing then serves storage: recording each message
  once, instead of every call's full history.
