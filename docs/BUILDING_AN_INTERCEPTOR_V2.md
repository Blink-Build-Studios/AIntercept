# Building an Interceptor on contract v2

Contract v2 ([`INTERCEPTOR_V2.md`](INTERCEPTOR_V2.md)) hands your Interceptor
the conversation instead of the wire: each call's new messages, in one shape
for Claude Code and Codex (HTTP or WebSocket), and each tool call the model
makes. You answer per message, and AIntercept puts the answer back on the
wire in the provider's format.

Everything about deploying, operating and observing an Interceptor is as in
[`BUILDING_AN_INTERCEPTOR.md`](BUILDING_AN_INTERCEPTOR.md) (sections 5 to
13); this guide covers what's different. The example is
`aintercept/examples/rabbit` (Go), about a hundred lines.

## 1. Generate code from the v2 contract

The contract is `aintercept/interceptor/v2/interceptor.proto` (package
`aintercept.interceptor.v2`). In Go, import it:

```go
import pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
```

In any other language, copy the file and generate a server for
`aintercept.interceptor.v2.Interceptor`, for example in Python:

```bash
python -m grpc_tools.protoc -I proto --python_out=. --grpc_python_out=. proto/interceptor/v2/interceptor.proto
```

## 2. The callbacks

| | What you get | What you answer |
|---|---|---|
| `OnCallStart` | Who, which client, which model (as v1) | Accept, or Respond |
| `OnRequest` | The call's **new** messages only (content on) | Per message Accept or Replace; for the call Respond; or Fetch |
| `OnToolCall` | Each tool call in the reply, complete, before the client sees it (enforce, content on) | Accept, Replace, or Respond |
| `OnCallComplete` | The finished call, with your answers and how a Respond was delivered | Ack |

An empty answer is Accept. There is no Deny or Drop: rewrite, or answer
for the model.

### OnRequest

A `ConvMessage` is one turn: a prompt, a reply, one tool call, one tool
result. Its parts are text, media, a tool call, a tool result, or opaque
(reasoning, encrypted content: present, never inspectable). Clients resend
the whole conversation on every call, but `OnRequest` carries only what this
call adds, usually one user prompt or the results of the tool calls the model
just made. You never re-check history.

To change a message, answer **Replace** with the message as you got it, every
part back by id and in order, with its text (or a tool result's text, or a
tool call's arguments) changed:

```go
func (server) OnRequest(_ context.Context, r *pb.RequestMessages) (*pb.RequestVerdict, error) {
	v := &pb.RequestVerdict{}
	for _, m := range r.GetMessages() {
		hit := false
		for _, p := range m.GetParts() {
			if t := p.GetText(); t != nil && secret.MatchString(t.GetText()) {
				t.Text, hit = secret.ReplaceAllString(t.GetText(), "[secret]"), true
			}
		}
		if hit {
			v.Messages = append(v.Messages, &pb.MessageVerdict{MessageId: m.GetId(),
				Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}}})
		}
	}
	return v, nil
}
```

AIntercept writes only what you changed back into the request; every other
byte goes upstream as sent. A Replace it can't apply (a part missing, an
image changed, arguments that aren't JSON) is an error, handled by
`onError`.

### Respond

Answer `RequestVerdict.respond` (or `CallVerdict.respond` at call start) with
text, and the client gets that text as the model's reply, in its own format,
streamed if it asked for a stream:

```go
v.Respond = &pb.Respond{Text: "Well, tricks are for kids."}
```

You don't choose how it's delivered. When the client keeps the conversation
(Claude Code without message threads, Codex without `previous_response_id`),
AIntercept synthesizes the reply and the call never reaches the provider.
When the provider keeps it, AIntercept rewrites the request to ask the model
for exactly your text, so the provider's history stays consistent. The
verdict row and `CallRecord.respond` say which (`synthesized` or
`rewritten`).

The rabbit answers the newest user message only: earlier messages in the
call are context, not what the user just asked.

### OnToolCall

Each tool call in the reply arrives once it's complete, before the client
sees any of it; the reply's text is never held. The message is the call
alone: an `ASSISTANT` message whose id is the provider's call id, with one
`ToolCall` part (`name`, `arguments` as JSON; a custom tool's free-form input
as a JSON string).

- **Accept** releases it exactly as the provider sent it.
- **Replace** with the message, its arguments changed (still valid JSON):
  the client runs the new ones.
- **Respond** ends the reply with your text instead of the call. The agent
  has nothing to run, so it stops and shows your text.

```go
func (server) OnToolCall(_ context.Context, r *pb.ResponseMessage) (*pb.MessageVerdict, error) {
	m := r.GetMessage()
	tc := m.GetParts()[0].GetToolCall()
	if strings.Contains(tc.GetArguments(), "rm -rf") {
		return &pb.MessageVerdict{Action: &pb.MessageVerdict_Respond{Respond: &pb.Respond{Text: "I won't run that."}}}, nil
	}
	return &pb.MessageVerdict{}, nil
}
```

`OnToolCall` is in the reply's path, under the same deadline as the others:
every tool call waits for it. If you don't need it, don't implement it (the
generated `Unimplemented` server is fine): AIntercept then stops holding tool
calls.

### Fetch

A part over the inline limit (64 KiB by default) arrives by reference: its
size and a 1 KiB preview, no content. If a rule needs the rest, answer
`fetch` with the part ids; `OnRequest` is called again with them inline, once
per call, inside the same deadline. A fetched part can be up to 32 MiB, so an
Interceptor that fetches raises its gRPC receive limit to 33 MiB; one that
never fetches keeps the default 4 MiB whatever the conversation holds.

## 3. Wire it in

In the Helm values, set the contract (the rest is as in
[`BUILDING_AN_INTERCEPTOR.md`](BUILDING_AN_INTERCEPTOR.md) section 8):

```yaml
interceptor:
  endpoint: my-interceptor.aintercept.svc:9100
  contract: v2
  content: true             # OnRequest and OnToolCall
  inlineLimitBytes: 65536   # larger parts go by reference
```

Without the chart: `INTERCEPTOR_CONTRACT=v2`, `INTERCEPTOR_CONTENT=true`,
`INTERCEPTOR_INLINE_LIMIT_BYTES`.

## 4. Test it

`make test-interceptor` runs the rabbit through a real Envoy as the v2
observer's Interceptor: the rabbit and the bing answered over Anthropic HTTP
(streamed and not), Codex HTTP and Codex's WebSocket; a chained request
rewritten; tool calls held, replaced and answered; the next turn after each.
`KEEP=1 make test-interceptor` leaves the stack up; the v2 observer's records
are in `test/interceptor/out/events-v2.ndjson`:

```bash
jq -c 'select(.phase=="verdict") | .verdict' test/interceptor/out/events-v2.ndjson
```

Unit-test your rules by calling your handlers with hand-built
`RequestMessages` and `ResponseMessage`s, as `examples/rabbit/main_test.go`
does.
