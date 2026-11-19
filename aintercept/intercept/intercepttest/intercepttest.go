// Package intercepttest tests an Interceptor built with package intercept:
// it runs the rules, hooks and watches exactly as the observer would, and
// reports what the client (or the model) would see.
//
//	h := intercepttest.New(rules...)
//	h.Prompt("…").WantReply(t, "…")
//	h.Reply("sword", "fi", "sh!").WantClientSees(t, "[password]!")
//	h.ToolCall("Bash", `{"command":"rm -rf /"}`).WantReply(t, "I won't run that.")
//	h.ToolResult("Bash", "…").WantReplaced(t, "…")
package intercepttest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
)

// Harness drives an Interceptor as AIntercept's observer would, in enforce
// mode, one call per message.
type Harness struct {
	s  *intercept.Server
	n  int
	id *pb.Identity
}

// As makes the calls that follow come from id (by default, an unknown
// caller): for rules that look at Call.Person.
func (h *Harness) As(id *pb.Identity) *Harness {
	h.id = id
	return h
}

// New builds the Interceptor from rules, hooks and watches, as intercept.Run
// does.
func New(items ...intercept.Item) *Harness { return &Harness{s: intercept.New(items...)} }

// Server is the Interceptor under test.
func (h *Harness) Server() *intercept.Server { return h.s }

// Result is what one message came to.
type Result struct {
	what string
	// Replied: the call was answered with Reply's text (Reply).
	replied bool
	reply   string
	// Changed: the message was replaced with Replaced.
	changed  bool
	replaced string
	// Client: what the client sees of a reply.
	client string
	err    error
}

func (h *Harness) call() *pb.Call {
	h.n++
	return &pb.Call{CallId: "intercepttest-" + itoa(h.n), Mode: pb.Mode_MODE_ENFORCE, Transport: pb.Transport_TRANSPORT_HTTP,
		Client: &pb.Client{Product: "claude_code"}, Identity: h.id}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// request runs OnRequest on one new message, with its Fetch round.
func (h *Harness) request(what string, cm *pb.ConvMessage) *Result {
	ctx := context.Background()
	c := h.call()
	r := &Result{what: what}
	if _, err := h.s.OnCallStart(ctx, &pb.CallStart{Call: c}); err != nil {
		r.err = err
		return r
	}
	req := &pb.RequestMessages{Call: c, Format: convo.FormatAnthropic, Messages: []*pb.ConvMessage{cm}, TotalMessages: 1}
	v, err := h.s.OnRequest(ctx, req)
	if err == nil && v.GetFetch() != nil {
		req.Fetched = true
		v, err = h.s.OnRequest(ctx, req)
	}
	if err != nil {
		r.err = err
		return r
	}
	if t := v.GetRespond().GetText(); t != "" {
		r.replied, r.reply = true, t
		return r
	}
	for _, mv := range v.GetMessages() {
		p := mv.GetReplace().GetMessage().GetParts()[0]
		r.changed, r.replaced = true, p.GetText().GetText()+p.GetToolResult().GetText()
	}
	return r
}

// Prompt is the user sending text.
func (h *Harness) Prompt(text string) *Result {
	return h.request("prompt", &pb.ConvMessage{Id: "m1", Role: pb.Role_ROLE_USER, Parts: []*pb.Part{
		{Id: "m1/0", Size: int64(len(text)), Kind: &pb.Part_Text{Text: &pb.Text{Text: text}}}}})
}

// Harness is the harness's own note going to the model (a system message,
// a system reminder in the user's turn).
func (h *Harness) Harness(text string) *Result {
	return h.request("harness note", &pb.ConvMessage{Id: "m1", Role: pb.Role_ROLE_SYSTEM, Parts: []*pb.Part{
		{Id: "m1/0", Size: int64(len(text)), Kind: &pb.Part_Text{Text: &pb.Text{Text: text}}}}})
}

// ToolResult is tool's result going back to the model.
func (h *Harness) ToolResult(tool, text string) *Result {
	return h.request("tool result", &pb.ConvMessage{Id: "m1", Role: pb.Role_ROLE_TOOL, Parts: []*pb.Part{
		{Id: "m1/0", Size: int64(len(text)), Kind: &pb.Part_ToolResult{ToolResult: &pb.ToolResult{CallId: "call_1", Name: tool, Text: text}}}}})
}

// ToolCall is the model asking to run tool with args (JSON).
func (h *Harness) ToolCall(tool, args string) *Result {
	r := &Result{what: "tool call"}
	msg := &pb.ResponseMessage{Call: h.call(), Message: &pb.ConvMessage{Id: "call_1", Role: pb.Role_ROLE_ASSISTANT, Parts: []*pb.Part{
		{Id: "call_1/0", Size: int64(len(args)), Kind: &pb.Part_ToolCall{ToolCall: &pb.ToolCall{CallId: "call_1", Name: tool, Arguments: args}}}}}}
	v, err := h.s.OnToolCall(context.Background(), msg)
	switch {
	case status.Code(err) == codes.Unimplemented:
	case err != nil:
		r.err = err
	case v.GetRespond() != nil:
		r.replied, r.reply = true, v.GetRespond().GetText()
	case v.GetReplace() != nil:
		r.changed, r.replaced = true, v.GetReplace().GetMessage().GetParts()[0].GetToolCall().GetArguments()
	}
	return r
}

// Reply is the model's reply text arriving in pieces, streamed (an Anthropic
// event stream), each decided as the observer decides it: the result is
// what the client sees.
func (h *Harness) Reply(pieces ...string) *Result {
	r := &Result{what: "reply"}
	c := h.call()
	dec := convo.Deciders{Text: func(p convo.TextPiece) convo.Answer {
		v, err := h.s.OnReply(context.Background(), &pb.ResponseMessage{Call: c, Released: p.Released, Done: p.Done,
			Message: &pb.ConvMessage{Id: p.ID, Role: pb.Role_ROLE_ASSISTANT, Parts: []*pb.Part{
				{Id: p.ID + "/0", Size: int64(len(p.Pending)), Kind: &pb.Part_Text{Text: &pb.Text{Text: p.Pending}}}}}})
		switch {
		case status.Code(err) == codes.Unimplemented:
			return convo.Answer{}
		case err != nil:
			r.err = err
			return convo.Answer{Verdict: convo.Stop}
		case v.GetRespond() != nil:
			r.replied, r.reply = true, v.GetRespond().GetText()
			return convo.Answer{Verdict: convo.Respond, Text: r.reply}
		case v.GetHold() != nil:
			return convo.Answer{Verdict: convo.Hold}
		case v.GetReplace() != nil:
			r.changed = true
			return convo.Answer{Verdict: convo.Replace, Text: v.GetReplace().GetMessage().GetParts()[0].GetText().GetText(), Keep: int(v.GetKeep())}
		}
		return convo.Answer{Keep: int(v.GetKeep())}
	}}
	s := convo.NewStream(convo.FormatAnthropic, convo.SSE, dec)
	ev := func(typ string, kv ...any) []byte {
		m := map[string]any{"type": typ}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		b, _ := json.Marshal(m)
		return b
	}
	var out []byte
	feed := func(data []byte) {
		out = append(out, s.Feed(eventType(data), data, convo.SSE(eventType(data), data))...)
		out = append(out, s.Pump()...)
	}
	feed(ev("message_start", "message", map[string]any{"id": "msg_intercepttest", "type": "message", "role": "assistant", "model": "m", "content": []any{}}))
	feed(ev("content_block_start", "index", 0, "content_block", map[string]any{"type": "text", "text": ""}))
	for _, p := range pieces {
		feed(ev("content_block_delta", "index", 0, "delta", map[string]any{"type": "text_delta", "text": p}))
	}
	feed(ev("content_block_stop", "index", 0))
	feed(ev("message_delta", "delta", map[string]any{"stop_reason": "end_turn"}))
	feed(ev("message_stop"))
	out = append(out, s.Flush()...)
	for _, line := range strings.Split(string(out), "\n") {
		d, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e struct {
			Type  string
			Delta struct{ Type, Text string }
		}
		_ = json.Unmarshal([]byte(d), &e)
		if e.Type == "content_block_delta" && e.Delta.Type == "text_delta" {
			r.client += e.Delta.Text
		}
	}
	return r
}

func eventType(data []byte) string {
	var e struct{ Type string }
	_ = json.Unmarshal(data, &e)
	return e.Type
}

func (r *Result) String() string {
	switch {
	case r.err != nil:
		return "failed: " + r.err.Error()
	case r.replied:
		return "answered with " + quote(r.reply)
	case r.changed && r.what != "reply":
		return "replaced with " + quote(r.replaced)
	case r.what == "reply":
		return "the client saw " + quote(r.client)
	}
	return "accepted"
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

// WantReply fails t unless the call was answered with text.
func (r *Result) WantReply(t testing.TB, text string) {
	t.Helper()
	if !r.replied || r.reply != text || r.err != nil {
		t.Fatalf("%s: %s, want a reply %q", r.what, r, text)
	}
}

// WantReplaced fails t unless the message was replaced with text (a tool
// call's arguments).
func (r *Result) WantReplaced(t testing.TB, text string) {
	t.Helper()
	if !r.changed || r.replaced != text || r.replied || r.err != nil {
		t.Fatalf("%s: %s, want it replaced with %q", r.what, r, text)
	}
}

// WantAccepted fails t unless the message went on unchanged.
func (r *Result) WantAccepted(t testing.TB) {
	t.Helper()
	if r.changed || r.replied || r.err != nil {
		t.Fatalf("%s: %s, want it accepted", r.what, r)
	}
}

// WantClientSees fails t unless the client saw text of the reply.
func (r *Result) WantClientSees(t testing.TB, text string) {
	t.Helper()
	if r.client != text || r.err != nil {
		t.Fatalf("%s: %s, want the client to see %q", r.what, r, text)
	}
}

// WantFailed fails t unless the Interceptor failed (onError applies).
func (r *Result) WantFailed(t testing.TB) {
	t.Helper()
	if r.err == nil {
		t.Fatalf("%s: %s, want a failure", r.what, r)
	}
}
