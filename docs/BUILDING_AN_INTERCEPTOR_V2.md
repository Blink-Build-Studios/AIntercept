# Writing an Interceptor

An Interceptor sees every message of every call, in both directions, before
it goes on: the prompts and tool results going to the model, and the model's
reply text (piece by piece, as it streams) and tool calls coming back. It
can let each one through, change it, or answer for the model. The contract
([`INTERCEPTOR.md`](INTERCEPTOR.md)) delivers them in one shape for
Claude Code, Codex (HTTP or WebSocket) and the OpenAI APIs; AIntercept puts
your answer back on the wire in the provider's format.

In Go, write it with package `intercept`: rules for the common case, hooks
for full control, and watches for phrases in streamed text. It's the protocol
underneath, wrapped: the gRPC server, the receive limits, health checks and
graceful shutdown are done for you. The raw protocol stays usable directly
(section 7), from any language.

Testing it through a real Envoy, deploying, operating and observing an
Interceptor are in [`BUILDING_AN_INTERCEPTOR.md`](BUILDING_AN_INTERCEPTOR.md)
(sections 5 to 13); this guide covers the code.

```go
import "github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
```

## 1. Rules

A rule is a condition and its answer. The example,
`aintercept/examples/rabbit`, is two of them: when the user's prompt says
the rabbit's phrase (the one ending "…silly rabbit"), it answers for the
model "Well, tricks are for kids."; when it says the bing phrase, "then we
bong". Everything else goes through, including tool results: a file the
agent reads that says a phrase doesn't set it off.

```go
var (
	rabbit = regexp.MustCompile(`(?i)i['’]?m a sil` + `ly rab` + `bit`)
	bing   = regexp.MustCompile(`(?i)fir` + `st we bi` + `ng`)
)

func main() {
	log.Fatal(intercept.Run(
		intercept.OnRequest(intercept.Prompt(intercept.Matches(rabbit))).Reply("Well, tricks are for kids."),
		intercept.OnRequest(intercept.Prompt(intercept.Matches(bing))).Reply("then we bong"),
	))
}
```

(The phrases are built from fragments so the source doesn't say them whole:
anything that quotes them would be answered.)

The rabbit has no rules on the model's reply, so AIntercept doesn't hold its
replies at all: an Interceptor pays only for what it handles.

| Rule | Applies to |
|---|---|
| `OnRequest(m)` | messages to the model: prompts, tool results, media |
| `OnResponse(m)` | messages from the model: its reply text and its tool calls |
| `OnMessage(m)` | both |
| `OnToolCallRequest(tool, m)` | the model asking to run `tool` ("" for any); a tool call *request* is in the model's response |
| `OnToolCallResponse(tool, m)` | `tool`'s result going back to the model; a tool call *response* is in the next request |

Each takes a matcher and ends with `.Reply(text)` (answer for the model),
`.Replace(text)` (replace what matched) or `.Do(func(*Message) Answer)`.
Matchers: `Contains(s)` (ignoring case), `Matches(re)`, `Prompt(m)` (only
what the user sent: `OnRequest` alone also sees the harness's notes, tool
results and media), `Arg(name, m)` (a tool call's argument), `Tool(name)`, `And`, `Or`, `Not`.

```go
intercept.Run(
	intercept.OnRequest(intercept.Prompt(intercept.Contains("magic word"))).Reply("No magic here."),
	intercept.OnMessage(intercept.Matches(apiKey)).Replace("[secret]"),
	intercept.OnResponse(intercept.Contains("swordfish")).Replace("[password]"),
	intercept.OnToolCallRequest("Bash", intercept.Arg("command", intercept.Contains("rm -rf"))).Reply("I won't run that."),
	intercept.OnToolCallResponse("Bash", intercept.Contains("BEGIN PRIVATE KEY")).Replace("[redacted]"),
)
```

A rule on the model's reply text with `Contains` or `Matches` becomes a watch
(section 3): a phrase split across streamed pieces is still caught. In a tool
call, `Replace` replaces inside the arguments' string values, so they stay
valid JSON. Rules run first, in order; the first that answers something
other than Accept wins.

## 2. Hooks

```go
intercept.Run(intercept.Hooks{
	OnCallStart:        func(ctx context.Context, c *intercept.Call) intercept.Answer { … },
	OnTurnStart:        func(ctx context.Context, t *intercept.Turn) intercept.Answer { … }, // each request on a WebSocket
	OnMessage:          func(ctx context.Context, m *intercept.Message) intercept.Answer { … }, // both directions, every kind
	OnRequest:          func(ctx context.Context, m *intercept.Message) intercept.Answer { … }, // to the model
	OnResponse:         func(ctx context.Context, m *intercept.Message) intercept.Answer { … }, // from the model: each reply text piece, tool calls
	OnToolCallRequest:  func(ctx context.Context, t *intercept.ToolCall) intercept.Answer { … },
	OnToolCallResponse: func(ctx context.Context, t *intercept.ToolResult) intercept.Answer { … },
	OnCallComplete:     func(ctx context.Context, r *intercept.Record) { … },
	Watches:            []intercept.Watch{…},
})
```

Every hook is optional, and rules and hooks combine (`Run(rule1, rule2,
intercept.Hooks{…})`). For each message, the most specific runs first, and
the first answer that isn't Accept wins; the rest are skipped:

| Message | Order |
|---|---|
| Prompt | rules → watches → `OnRequest` → `OnMessage` |
| Tool result | rules → watches → `OnToolCallResponse` → `OnRequest` → `OnMessage` |
| Reply text (each piece) | watches (rules on reply text among them) → `OnResponse` → `OnMessage` |
| Tool call | rules → `OnToolCallRequest` → `OnResponse` → `OnMessage` |
| Harness note | rules → watches → `OnRequest` → `OnMessage` |
| Opaque part (reasoning) | `OnResponse` → `OnMessage`; the answer is ignored |

If nothing applies to the reply's text, or to its tool calls, the library
tells AIntercept not to hold them (it answers the first such callback
UNIMPLEMENTED, and AIntercept stops asking).

Per call, hooks run in order: start, then the messages in order both ways,
then complete. Calls run concurrently. `ctx` carries the deadline.

**`Call`** is the same object in every hook of one call (a Codex WebSocket
connection is one call for all its turns): `ID`, `Mode` (`Observe`,
`Enforce`), `Person` (`Email`, `Name`, `Account`, and `Verified`: the provider confirmed them for the call's credential; grant privileges on a verified Person only), `Agent` (`ClaudeCode`, `Codex`),
`Model`, `Session`, `Transport` (`HTTP`, `WebSocket`), `Provider` and `API`
(who serves the call, and which of its APIs it's on), `Options` (what the
request asks for: `c.Options.Get(intercept.Speed)`; below, "Options"), and
per-call state:
`c.Set(k, v)`, `c.Get(k)`, freed after the call completes. The state lives in
the process: run one replica, or keep state you need across replicas
elsewhere.

**`Message`**: `Direction` (`ToModel`, `FromModel`), `Kind` (`KindPrompt`,
`KindToolResult`, `KindText`, `KindToolCall`, `KindMedia`, `KindOpaque`,
`KindHarness`),
`Text`, `Tool`, `Call`, `Newest` (the prompt the user just sent), `Truncated`
and `Size` (content over the inline limit: `Text` is its start), `ID`, and
for reply text `Before` (the block's text already released to the client)
and `Final` (the block's last piece). `Changeable()` says whether an answer
can take effect (enforce mode, and not media or opaque). Helpers:
`Contains`, `Matches`, `IsPrompt`, `IsToolCall(name)`, `Command()`, `Path()`,
`ReplaceAll(re, repl)`.

**What the user sent, and what the harness added.** A user's message to the
model goes through the harness (Claude Code, Codex), which adds its own text:
system reminders, notes that a file changed or tools were added, Codex's
environment. That text is `KindHarness`, never `KindPrompt`, so `Prompt(m)`
matches only what the user sent. When the user types while Claude Code is
working, `Text` is just their words, without Claude Code's wrapper (and a
`Replace` keeps the wrapper). Rules, watches and hooks see `KindHarness`
messages too, and can replace or answer them like any other.

**`ToolCall`**: `Name`, `Server` (an MCP tool's server), `Args`, `Arg(name)`,
`Command()` (a shell command, Claude Code's or Codex's), `Path()`, plus
`Message` and `Call`. **`ToolResult`**: `Name` (the tool it answers, matched
by call id from the request's history), `Text`, `IsError`, `ExitCode` (when
the result says), plus `Message` and `Call`.

**`Record`**: `Call`, `Usage` (`Input`, `Output`, `CacheRead`, `CacheWrite`,
`Reasoning`), `Status`, `Model`, `StopReason`, `Asked` and `Served` (the
options the request asked for, and those the provider reports serving it
with), `TTFB`, `Duration`, `Answers`
(every decision recorded since the call's previous record, per message),
`Replied` (`synthesized` or `rewritten`) and `WSTurn`.

**Answers**, the same everywhere: `Accept()` (or nil), `Replace(text)`,
`ReplaceArgs(json)` for a tool call, `Reply(text)` (answer for the model;
AIntercept picks how to deliver it), `Hold()` (reply text only: wait for
more), `NeedFull()` (a `Truncated` message: call again with all of it),
`Error(status, type, message)` (refuse the call, a WebSocket turn or a
request: the client gets an error written as the call's API writes its own;
not for the model's reply), and `Fail(err)` (AIntercept's `onError` applies:
refuse the call, or let it through; a panic is recovered the same way). Any
answer takes `.Note(key, value)`, recorded with the decision.

### Options: per-person fast mode

A call's provider settings arrive in `c.Options`, read for you from where
each API puts them ([`INTERCEPTOR.md`](INTERCEPTOR.md), "Options"):
`intercept.Effort`, `intercept.Speed` (Anthropic's fast mode: `"fast"`, or
absent), `intercept.ServiceTier` and `intercept.Thinking`. `Get` returns ""
for a setting the request doesn't make. They need no Interceptor content.

Anthropic's fast mode is on or off for a whole organization. To allow it per
person, refuse a fast request from anyone else with Anthropic's own refusal.
Claude Code then retries at standard speed, as it does when Anthropic itself
refuses:

```go
func fastFor(allowed map[string]bool) intercept.Hooks {
	refuse := func(o intercept.Options, p intercept.Person) intercept.Answer {
		if o.Get(intercept.Speed) == "fast" && !(p.Verified && allowed[p.Email]) {
			return intercept.Error(400, "invalid_request_error", "Fast mode is not enabled for your organization.")
		}
		return nil
	}
	return intercept.Hooks{
		OnCallStart: func(_ context.Context, c *intercept.Call) intercept.Answer { return refuse(c.Options, c.Person) },
		// Codex's WebSocket: each request (turn) has its own options.
		OnTurnStart: func(_ context.Context, t *intercept.Turn) intercept.Answer { return refuse(t.Options, t.Call.Person) },
	}
}
```

**`Turn`** is one request on a Codex WebSocket: `N` (its number on the
connection, the record's `WSTurn`), `Model`, `Options`, and the connection's
`Call`. Without an `OnTurnStart` hook the library answers it UNIMPLEMENTED,
and AIntercept stops asking (and keeps the WebSocket's compression).

### Reply text, piece by piece

`OnResponse` and `OnMessage` get the model's reply text as it streams, one
call per piece (pieces that arrive while you're deciding come together in
the next call), before the client sees any of it. `Text` is the new text,
`Before` what's already gone to the client. Accept releases it, `Replace`
releases your text instead, `Hold` keeps it and asks again with what comes
next (with `Final`, nothing can stay held: a Hold releases it), `Reply` ends
the reply with your text. Example 6.6 holds each sentence until it's whole.

## 3. Watches

A watch finds a phrase (ignoring case) or a pattern in messages, including
reply text as it streams: a match split across pieces is seen whole. Only the
tail that could still become a match is held back; the text before it goes
on at once.

```go
intercept.Run(intercept.Hooks{Watches: []intercept.Watch{{
	Phrase: "swordfish",          // or Pattern: re, MaxLen: the longest match
	In:     intercept.FromModel,  // 0: both directions
	On: func(ctx context.Context, w *intercept.Match) intercept.Answer {
		return w.Replace("[password]") // the match alone; w.Reply(text) ends the reply there
	},
}}})
```

Messages to the model arrive whole, so there a watch simply runs on the text.
A pattern holds back its `MaxLen` less one bytes (a `Matches` rule on reply
text uses 256). In observe mode nothing is held back, so the library also
looks at the end of what was already released.

## 4. Run it, wire it in

`intercept.Run` listens on `INTERCEPT_LISTEN` (default `:9100`) with the
receive limit `NeedFull` needs (33 MiB), gRPC health checks, a JSON log line
per decision (other than a plain Accept) and, with
`INTERCEPT_METRICS_LISTEN`, decision counts at `/debug/vars`. It stops
gracefully on SIGINT or SIGTERM. `intercept.New(…)` gives the server itself
(`pb.InterceptorServer`; `.GRPC()` for a ready gRPC server).

In the Helm values (the rest is in
[`BUILDING_AN_INTERCEPTOR.md`](BUILDING_AN_INTERCEPTOR.md) section 8):

```yaml
interceptor:
  endpoint: my-interceptor.aintercept.svc:9100
  content: true             # the messages; off: only call start and complete
  inlineLimitBytes: 65536   # larger parts arrive truncated (NeedFull for all of it)
```

Without the chart: `INTERCEPTOR_ENDPOINT`, `INTERCEPTOR_CONTENT=true`,
`INTERCEPTOR_INLINE_LIMIT_BYTES`.

## 5. Test it

`intercepttest` runs your rules and hooks exactly as AIntercept's observer
would, and reports what the client (or the model) sees:

```go
h := intercepttest.New(rules...)
h.Prompt("say the magic word").WantReply(t, "No magic here.")
h.Reply("The password is sword", "fi", "sh!").WantClientSees(t, "The password is [password]!")
h.ToolCall("Bash", `{"command":"rm -rf /"}`).WantReply(t, "I won't run that.")
h.ToolResult("Bash", "-----BEGIN PRIVATE KEY-----").WantReplaced(t, "-----[redacted]-----")
h.Prompt("hello").WantAccepted(t)
// A rule on who's calling: the calls that follow come from this identity.
h.As(&pb.Identity{Email: "a@example.com", EmailSource: pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL}).Prompt("hello").WantAccepted(t)
```

`examples/rabbit/main_test.go` and `examples/redact/main_test.go` are
complete ones. `make test-interceptor` runs AIntercept end to end through a
real Envoy with the example redact, and with the suite's own Interceptor
(`aintercept/internal/e2e/interceptor`: the rabbit's rules plus rules on
reply text and tool calls), in every format. `KEEP=1` leaves the stack up;
the second observer's records (the suite's Interceptor) are in
`test/interceptor/out/events-v2.ndjson`:

```bash
jq -c 'select(.phase=="verdict") | .verdict' test/interceptor/out/events-v2.ndjson
```

## 6. Every interaction, worked through

Each example below is taken from a run: AIntercept's enforcing filter
(`internal/extproc`) with a package `intercept` Interceptor over gRPC and a
scripted provider. For each: what the client sent, what your hook got and
answered, what went upstream or to the client, and what `OnCallComplete`
reported. Long bodies are trimmed with `…`; nothing else is changed. The data
is synthetic. Unless it says otherwise, the client is Claude Code (Anthropic
Messages, streamed), the mode is enforce and Interceptor content is on.

### 6.1 A rule answers a prompt (synthesized)

`intercept.OnRequest(intercept.Contains("magic word")).Reply("No magic here.")`

The client sends its next prompt, with the history:

```json
{"model":"claude-sonnet-4-5","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"What does main.go do?"},{"role":"assistant","content":"It starts the server."},{"role":"user","content":[{"type":"text","text":"Say the magic word.","cache_control":{"type":"ephemeral"}}]}]}
```

The rule sees only the new message: `Direction: ToModel, Kind: KindPrompt,
Text: "Say the magic word.", Newest: true`. It answers `Reply("No magic
here.")`. Claude Code keeps the conversation itself, so the reply is
synthesized: the call never reaches the provider, and the client gets a
normal `200 text/event-stream` reply:

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_01TY1nn9i92p75SFyGrJULdN","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"No magic here."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}

event: message_stop
data: {"type":"message_stop"}
```

`OnCallComplete`'s `Record`: zero usage (the model never ran), and how the
reply was delivered:

```json
{"Usage":{"Input":0,"Output":0,"CacheRead":0,"CacheWrite":0,"Reasoning":0},"Status":200,"Model":"claude-sonnet-4-5","StopReason":"end_turn","Replied":"synthesized","Answers":[{"Callback":"request","Action":"respond","Applied":true,"RespondMode":"synthesized"}]}
```

### 6.2 The same, when the provider keeps the conversation (rewritten)

With Claude Code's message threads the provider keeps the history, so a
reply it never generated would break the next call. The same rule's `Reply`
is delivered by asking the model for exactly the text:

```json
CLIENT:   {"model":"claude-sonnet-4-5","stream":true,"thread":{"previous_message_id":"msg_01Pq"},"messages":[{"role":"user","content":"Say the magic word."}]}
UPSTREAM: {"model":"claude-sonnet-4-5","stream":true,"thread":{"previous_message_id":"msg_01Pq"},"messages":[{"role":"user","content":"Reply with exactly this text and nothing else: No magic here."}]}
```

The model's reply streams back to the client as it came (this Interceptor
has no rules on reply text). The record has the provider's real usage:

```json
{"Usage":{"Input":2140,"Output":12,"CacheRead":18000,"CacheWrite":0,"Reasoning":0},"Status":200,"StopReason":"end_turn","Replied":"rewritten","Answers":[{"Callback":"request","Action":"respond","Applied":true,"RespondMode":"rewritten"}]}
```

### 6.3 A hook replaces a key in a prompt, and sees the reply

```go
OnMessage: func(_ context.Context, m *intercept.Message) intercept.Answer {
	return m.ReplaceAll(apiKey, "[secret]")
},
```

The prompt `Use sk-live-4f9a8b7c6d5e to call the API.` reaches `OnMessage`
as `Direction: ToModel, Kind: KindPrompt, Newest: true`. The answer is
`Replace("Use [secret] to call the API.")`, and only that text changes in
the body that goes upstream:

```json
{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"Use [secret] to call the API."}]}
```

`OnMessage` sees both directions, so it also gets the reply, piece by piece,
before the client does:

| Provider sends | `OnMessage` gets | Answer | Client gets |
|---|---|---|---|
| `text_delta "Done: I"` | `FromModel, KindText, Text: "Done: I", Before: ""` | Accept | the event, as sent |
| `text_delta " called it."` | `FromModel, KindText, Text: " called it.", Before: "Done: I"` | Accept | the event, as sent |

```json
{"Usage":{"Input":2140,"Output":12,"CacheRead":18000,"CacheWrite":0,"Reasoning":0},"Status":200,"StopReason":"end_turn","Answers":[{"Callback":"request","Action":"replace","Applied":true,"MessageID":"6f3e47886f7347fa5f8e2eafb70c2eb8"}]}
```

### 6.4 A tool's result is replaced before the model sees it

```go
OnToolCallResponse: func(_ context.Context, r *intercept.ToolResult) intercept.Answer {
	if strings.Contains(r.Text, "BEGIN PRIVATE KEY") {
		return intercept.Replace("[a private key, redacted]")
	}
	return nil
},
```

Claude Code sends the result of the `cat deploy.pem` it ran:

```json
{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"Show me the deploy key."},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"cat deploy.pem"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----"}]}]}
```

The hook gets `Name: "Bash"` (matched by the call id from the history),
`Text: "-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----"`. The
model gets the result replaced, every other byte as sent:

```json
…{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"[a private key, redacted]"}]}]}
```

### 6.5 A phrase in the reply, split across pieces

`intercept.OnResponse(intercept.Contains("swordfish")).Replace("[password]")`
becomes a watch. The model's reply arrives in three pieces:

| Provider sends | Pending (`OnReply`) | Answer | Client gets |
|---|---|---|---|
| `"The password is sw"` | `The password is sw` | Accept, `keep: 2` (`sw` could start the phrase) | `"The password is "` |
| `"ordf"` | `swordf` (released: `The password is `) | Accept, `keep: 6` | nothing yet |
| `"ish, keep it safe."` | `swordfish, keep it safe.` | Replace: `[password], keep it safe.` | `"[password], keep it safe."` |

Nothing of the phrase reaches the client, and the text before it wasn't
delayed:

```
event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The password is "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[password], keep it safe."}}
```

```json
{"Usage":{"Input":2140,"Output":12,"CacheRead":18000,"CacheWrite":0,"Reasoning":0},"Status":200,"StopReason":"end_turn","Answers":[{"Callback":"reply","Action":"replace","Applied":true,"MessageID":"msg_01XFDUDYJgAACzvnptvVoYEL:0"}]}
```

### 6.6 A hook holds the reply until each sentence is whole

```go
OnResponse: func(_ context.Context, m *intercept.Message) intercept.Answer {
	if !m.Final && !strings.HasSuffix(strings.TrimSpace(m.Text), ".") {
		return intercept.Hold()
	}
	return intercept.Replace(strings.ReplaceAll(m.Text, "Q3", "[quarter]"))
},
```

| Provider sends | `OnResponse` gets | Answer | Client gets |
|---|---|---|---|
| `"Revenue in Q"` | `Text: "Revenue in Q", Before: ""` | Hold | nothing yet |
| `"3 grew."` | `Text: "Revenue in Q3 grew.", Before: ""` | Replace `Revenue in [quarter] grew.` | `"Revenue in [quarter] grew."` |
| `" Costs fell."` | `Text: " Costs fell.", Before: "Revenue in [quarter] grew."` | Replace (unchanged: Accept) | the event, as sent |

The record lists both answers on the block: `hold`, then `replace`.

### 6.7 A watch ends the reply

```go
Watches: []intercept.Watch{{Phrase: "rm -rf /", In: intercept.FromModel,
	On: func(_ context.Context, w *intercept.Match) intercept.Answer { return w.Reply("I can't suggest that.") }}},
```

| Provider sends | Answer | Client gets |
|---|---|---|
| `"You could run rm -r"` | Accept, `keep: 5` | `"You could run "` |
| `"f / and then"` | the watch matches `rm -rf /`: Reply | `"I can't suggest that."`, then the block's stop, `end_turn`, `message_stop` |
| `" reinstall."`, the provider's own ending | | nothing: dropped |

The record keeps the provider's real usage (it did generate the reply) and
says the reply was ended: `"Replied":"synthesized"`,
`"Answers":[{"Callback":"reply","Action":"respond","Applied":true,"MessageID":"msg_01XFDUDYJgAACzvnptvVoYEL:0","RespondMode":"synthesized"}]`.

### 6.8 A tool call answered for the model

`intercept.OnToolCallRequest("Bash", intercept.Arg("command", intercept.Contains("rm -rf"))).Reply("I won't run that.")`

The reply's text goes on (no rules on text); the `Bash` call is held from its
`content_block_start` until its `content_block_stop`, then decided: its
`command` is `rm -rf build/ /`. The client gets the text, then the answer in
place of the call, and the reply ends; Claude Code has nothing to run:

```
event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Removing the build output."}}
…
event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I won't run that."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}

event: message_stop
data: {"type":"message_stop"}
```

```json
{"Usage":{"Input":3000,"Output":45,"CacheRead":0,"CacheWrite":0,"Reasoning":0},"Status":200,"StopReason":"tool_use","Replied":"synthesized","Answers":[{"Callback":"tool_call","Action":"respond","Applied":true,"MessageID":"toolu_01Rm","RespondMode":"synthesized"}]}
```

### 6.9 A tool call's arguments changed (Codex over HTTP)

```go
OnToolCallRequest: func(_ context.Context, tc *intercept.ToolCall) intercept.Answer {
	if strings.Contains(tc.Command(), "git push --force") {
		return intercept.ReplaceArgs(`{"command":["bash","-lc","git push"]}`)
	}
	return nil
},
```

Codex's `shell` call arrives as `Name: "shell"`, `Args: {"command":
["bash","-lc","git push --force"]}`, `Command(): "git push --force"`. Its
events are held until `response.output_item.done`; then they go out with the
new arguments in every event that carries them (and in the final
`response.completed`), and Codex runs `git push`:

```
data: {"type":"response.output_item.added",…,"item":{"id":"fc_68a1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_Xb2","name":"shell"}}
data: {"type":"response.function_call_arguments.delta",…,"delta":"{\"command\":[\"bash\",\"-lc\",\"git push\"]}"}
data: {"type":"response.function_call_arguments.done",…,"arguments":"{\"command\":[\"bash\",\"-lc\",\"git push\"]}"}
data: {"type":"response.output_item.done",…,"item":{…,"arguments":"{\"command\":[\"bash\",\"-lc\",\"git push\"]}",…}}
```

```json
{"Usage":{"Input":5120,"Output":40,"CacheRead":4096,"CacheWrite":0,"Reasoning":24},"Status":200,"Model":"gpt-5-codex","StopReason":"completed","Answers":[{"Callback":"tool_call","Action":"replace","Applied":true,"MessageID":"call_Xb2"}]}
```

### 6.10 Codex's WebSocket: one call, a record per turn

The magic-word rule again, on a Codex WebSocket connection. `OnCallStart`
comes once, at the upgrade (`Agent: "codex"`, `Transport: WebSocket`); each
`response.create` is a turn, and each turn's new messages go to the rules.

The first turn says the magic word: it never goes upstream, and Codex gets
the reply as the provider's own events on the socket (`response.created` …
`response.output_text.delta "No magic here."` … `response.completed` with
zero usage). The second turn (`List the files.`) goes upstream as sent, and
its reply streams back as it came.

`OnCallComplete` comes once per turn, then once when the connection closes,
each with that turn's own answers and usage:

```json
{"Usage":{"Input":0,"Output":0,…},"Status":101,"Model":"gpt-5-codex","StopReason":"completed","Replied":"synthesized","WSTurn":1,"Answers":[{"Callback":"request","Action":"respond","Applied":true,"RespondMode":"synthesized"}]}
{"Usage":{"Input":900,"Output":3,…},"Status":101,"Model":"gpt-5-codex","StopReason":"completed","WSTurn":2,"Answers":null}
{"Usage":{"Input":0,"Output":0,…},"Status":101,"WSTurn":0,"Answers":null}
```

### 6.11 A message over the inline limit (NeedFull)

```go
OnRequest: func(_ context.Context, m *intercept.Message) intercept.Answer {
	if m.Truncated {
		return intercept.NeedFull()
	}
	return m.ReplaceAll(apiKey, "[secret]")
},
```

With `inlineLimitBytes: 64`, a 124-byte prompt arrives with `Truncated:
true`, `Size: 124` and `Text` holding its start. `NeedFull` asks for it
whole; the hook is called again with `Truncated: false` and all 124 bytes,
answers `Replace`, and the key is replaced upstream:

```json
{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"log line ok. log line ok. … log line ok. [secret]"}]}
```

Both rounds are inside the same deadline.

### 6.12 The Interceptor fails

```go
OnRequest: func(context.Context, *intercept.Message) intercept.Answer {
	return intercept.Fail(errors.New("policy service unreachable"))
},
```

With `onError: deny` (the default) the call is refused with the provider's
own error shape, and the record says why:

```
503 application/json
{"error":{"message":"interceptor unavailable","type":"permission_error"},"type":"error"}
```

```json
{"Status":503,"Model":"claude-sonnet-4-5","Answers":[{"Callback":"request","Action":"error","Applied":true,"Reason":"rpc error: code = Aborted desc = intercept: policy service unreachable"}]}
```

With `onError: allow` the call goes on as sent, and the error is recorded
with `Applied: false`. The same holds in the reply: a failure on reply text
or a tool call ends the reply with the provider's error event (deny) or
releases what was held (allow).

### 6.13 Content off

With `interceptor.content: false`, the Interceptor gets only `OnCallStart`
(and a WebSocket's `OnTurnStart`) and `OnCallComplete`: no messages, so the
magic-word rule never runs and the call goes upstream as sent. Rules on who
calls, the model and its options (an Error for fast mode) still apply. The record still has everything about the call:

```json
{"Usage":{"Input":2140,"Output":12,"CacheRead":18000,"CacheWrite":0,"Reasoning":0},"Status":200,"Model":"claude-sonnet-4-5","StopReason":"end_turn","Answers":null}
```

### 6.14 Observe mode

The same Interceptor (the magic-word rule and the swordfish watch), observed:
the call goes upstream and its reply to the client exactly as sent, and the
callbacks come afterwards, queued, with `Call.Mode == Observe` and
`Changeable()` false. The answers are recorded, never applied:

```json
{"Usage":{"Input":2140,"Output":12,"CacheRead":18000,"CacheWrite":0,"Reasoning":0},"Status":200,"StopReason":"end_turn","Answers":[{"Callback":"request","Action":"respond","Applied":false},{"Callback":"reply","Action":"replace","Applied":false,"MessageID":"msg_01XFDUDYJgAACzvnptvVoYEL:0"}]}
```

Nothing is held back in observe mode, so the second piece (`fish.`) arrives
with `Before: "The password is sword"`; the watch looks at the end of
`Before` too, and still finds the phrase.

### 6.15 Reasoning (opaque)

A reply with a `thinking` block, then text. `OnResponse` gets the thinking
block once, in order, as `Kind: KindOpaque, Final: true, Changeable: false`:
it can see that the model reasoned, not what. It goes to the client as it
came, whatever the answer. Then the text: `Kind: KindText, Text: "42."`.

## 7. The raw protocol

The library is a gRPC server for `aintercept.interceptor.v2.Interceptor`
(`aintercept/interceptor/v2/interceptor.proto`). In another language, copy
the file and generate a server, for example in Python:

```bash
python -m grpc_tools.protoc -I proto --python_out=. --grpc_python_out=. proto/interceptor/v2/interceptor.proto
```

In Go, `pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"`.

| Callback | What you get | What you answer |
|---|---|---|
| `OnCallStart` | `CallStart`: who, which client, which model, its options | `CallVerdict`: Accept, `respond` or `error` |
| `OnTurnStart` | `TurnStart`: a WebSocket's request: its turn, model and options | `CallVerdict`: Accept, `respond` or `error` |
| `OnRequest` | `RequestMessages`: the call's new messages (content on) | `RequestVerdict`: per message Accept or Replace; `respond`; `error`; or `fetch` |
| `OnReply` | `ResponseMessage`: a text block's pending text, with `released` and `done`; or an opaque part | `MessageVerdict`: Accept, Replace, Hold or Respond, and `keep` |
| `OnToolCall` | `ResponseMessage`: one complete tool call | `MessageVerdict`: Accept, Replace (new arguments) or Respond |
| `OnCallComplete` | `CallRecord`: usage, timings, your answers | `Ack` |

An empty answer is Accept. There is no Drop. Not implementing
`OnTurnStart`, `OnReply` or `OnToolCall` (UNIMPLEMENTED) accepts what it
would get, and AIntercept stops holding it. [`INTERCEPTOR.md`](INTERCEPTOR.md) is the full
specification, and `aintercept/examples/python-redact` a complete
Interceptor on the raw protocol, in Python.

What the library got in 6.4 (`call` left out; it's on every callback):

```textproto
format: "anthropic_messages"
messages: {
  id: "85d2f7326fb843a8e200217e11685b3f"
  role: ROLE_TOOL
  parts: {
    tool_result: {
      call_id: "toolu_01A"
      text: "-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----"
      name: "Bash"
    }
    id: "85d2f7326fb843a8e200217e11685b3f/0"
    size: 60
  }
  index: 2
}
total_messages: 3
```

To change a message, answer Replace with it as you got it, every part back
by id and in order, its text (a tool result's text, a tool call's arguments)
changed:

```textproto
messages: {
  message_id: "85d2f7326fb843a8e200217e11685b3f"
  replace: { message: { id: "85d2f7326fb843a8e200217e11685b3f" role: ROLE_TOOL
    parts: { tool_result: { call_id: "toolu_01A" text: "[a private key, redacted]" name: "Bash" } id: "85d2f7326fb843a8e200217e11685b3f/0" } } }
}
```

The third `OnReply` of 6.5, and its answer:

```textproto
message: {
  id: "msg_01XFDUDYJgAACzvnptvVoYEL:0"
  role: ROLE_ASSISTANT
  parts: {
    text: { text: "swordfish, keep it safe." }
    id: "msg_01XFDUDYJgAACzvnptvVoYEL:0/0"
    size: 24
  }
}
released: "The password is "
```

```textproto
message_id: "msg_01XFDUDYJgAACzvnptvVoYEL:0"
replace: { message: { id: "msg_01XFDUDYJgAACzvnptvVoYEL:0" role: ROLE_ASSISTANT
  parts: { text: { text: "[password], keep it safe." } id: "msg_01XFDUDYJgAACzvnptvVoYEL:0/0" } } }
```

(the first two answers were Accept with `keep: 2`, then `keep: 6`). The
`OnToolCall` of 6.8, and its answer:

```textproto
message: {
  id: "toolu_01Rm"
  role: ROLE_ASSISTANT
  parts: {
    tool_call: { call_id: "toolu_01Rm" name: "Bash" arguments: "{\"command\":\"rm -rf build/ /\"}" }
    id: "toolu_01Rm/0"
    size: 29
  }
}
```

```textproto
message_id: "toolu_01Rm"
respond: { text: "I won't run that." }
```

A part over the inline limit arrives with `ref: true`, its `size` and a 1 KiB
`preview`, and no content; answer `fetch` with its id and `OnRequest` is
called again with it inline (once per call, inside the same deadline). A
fetched part can be up to 32 MiB, so an Interceptor that fetches raises its
gRPC receive limit to 33 MiB; one that never fetches keeps the default 4 MiB
whatever the conversation holds.
