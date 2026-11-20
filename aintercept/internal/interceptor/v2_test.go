package interceptor_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

func parse(t *testing.T, body string) convo.Request {
	t.Helper()
	r, ok := convo.Parse("api.anthropic.com", "/v1/messages", []byte(body))
	if !ok {
		t.Fatalf("not parsed: %s", body)
	}
	return r
}

var call = &pb.Call{CallId: "c1", Identity: &pb.Identity{SessionId: "sess-1"}}

// A conversation with history, then a tool result and a note: only those two
// arrive, with their ids, indexes, roles and parts.
const withHistory = `{"model":"m","messages":[` +
	`{"role":"user","content":"list the files"},` +
	`{"role":"assistant","content":[{"type":"text","text":"sure"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.txt"},{"type":"text","text":"I'm a silly rabbit"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}}]}]}`

func TestV2OnlyNewMessages(t *testing.T) {
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	r := parse(t, withHistory)
	if d := c.DecideRequest(context.Background(), call, r); d.Action != interceptor.Allow {
		t.Fatalf("decision %+v, want Allow", d)
	}
	_, reqs, _ := f.Snapshot()
	if len(reqs) != 1 {
		t.Fatalf("%d OnRequest calls, want 1", len(reqs))
	}
	q := reqs[0]
	ids := r.IDs("sess-1")
	if q.GetCall().GetCallId() != "c1" || q.GetFormat() != "anthropic_messages" || q.GetChained() || q.GetTotalMessages() != 5 || q.GetFetched() {
		t.Fatalf("request %v", q)
	}
	if len(q.GetMessages()) != 2 {
		t.Fatalf("got %d messages, want the 2 new ones: %v", len(q.GetMessages()), q.GetMessages())
	}
	res, note := q.GetMessages()[0], q.GetMessages()[1]
	if res.GetId() != ids[3] || res.GetIndex() != 3 || res.GetRole() != pb.Role_ROLE_TOOL ||
		res.GetParts()[0].GetToolResult().GetText() != "a.txt" || res.GetParts()[0].GetToolResult().GetCallId() != "t1" || res.GetParts()[0].GetId() != ids[3]+"/0" {
		t.Fatalf("tool result %v", res)
	}
	if note.GetId() != ids[4] || note.GetIndex() != 4 || note.GetRole() != pb.Role_ROLE_USER || len(note.GetParts()) != 2 ||
		note.GetParts()[0].GetText().GetText() != "I'm a silly rabbit" || note.GetParts()[1].GetId() != ids[4]+"/1" {
		t.Fatalf("note %v", note)
	}
	if m := note.GetParts()[1].GetMedia(); m.GetMediaType() != "image/png" || !bytes.Equal(m.GetData(), []byte{0, 1, 2}) || note.GetParts()[1].GetSize() != 3 {
		t.Fatalf("image %v", note.GetParts()[1])
	}
}

// replaceText answers with the message whose text part says from, said to.
func replaceText(from, to string) func(*pb.RequestMessages) *pb.RequestVerdict {
	return func(q *pb.RequestMessages) *pb.RequestVerdict {
		v := &pb.RequestVerdict{}
		for _, m := range q.GetMessages() {
			m := proto.Clone(m).(*pb.ConvMessage)
			changed := false
			for _, p := range m.GetParts() {
				if t := p.GetText(); t != nil && strings.Contains(t.GetText(), from) {
					t.Text = strings.ReplaceAll(t.GetText(), from, to)
					changed = true
				}
			}
			if changed {
				v.Messages = append(v.Messages, &pb.MessageVerdict{MessageId: m.GetId(),
					Action:      &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}},
					Annotations: []*pb.Annotation{{Key: "rule", Value: "rabbit"}}})
			}
		}
		return v
	}
}

func TestV2ReplaceRewritesTheRequest(t *testing.T) {
	f := &interceptortest.Fake{Request: replaceText("I'm a silly rabbit", "tricks")}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	r := parse(t, withHistory)
	d := c.DecideRequest(context.Background(), call, r)
	if want := strings.Replace(withHistory, "I'm a silly rabbit", "tricks", 1); d.Action != interceptor.Replace || string(d.Payload) != want {
		t.Fatalf("decision %v %s\nwant %s", d.Action, d.Payload, want)
	}
	got := rw.all()
	if len(got) != 1 || got[0].Callback != "request" || got[0].Action != "replace" || !got[0].Applied || got[0].Mode != "enforce" ||
		got[0].MessageID != r.IDs("sess-1")[4] || got[0].Annotations["rule"] != "rabbit" {
		t.Fatalf("verdict rows %+v", got)
	}
}

// Fetch: OnRequest again, with the part's content inline; then the answer.
func TestV2Fetch(t *testing.T) {
	big := strings.Repeat("x", 100<<10)
	body := `{"messages":[{"role":"user","content":"` + big + `"}]}`
	f := &interceptortest.Fake{}
	f.Request = func(q *pb.RequestMessages) *pb.RequestVerdict {
		p := q.GetMessages()[0].GetParts()[0]
		if !q.GetFetched() {
			return &pb.RequestVerdict{Fetch: &pb.Fetch{PartIds: []string{p.GetId()}}}
		}
		return replaceText(big, "short")(q)
	}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	d := c.DecideRequest(context.Background(), call, parse(t, body))
	if d.Action != interceptor.Replace || string(d.Payload) != `{"messages":[{"role":"user","content":"short"}]}` {
		t.Fatalf("decision %v %.100s", d.Action, d.Payload)
	}
	_, reqs, _ := f.Snapshot()
	if len(reqs) != 2 {
		t.Fatalf("%d OnRequest calls, want 2", len(reqs))
	}
	first, second := reqs[0].GetMessages()[0].GetParts()[0], reqs[1].GetMessages()[0].GetParts()[0]
	if !first.GetRef() || first.GetText().GetText() != "" || first.GetSize() != int64(len(big)) || len(first.GetPreview()) != 1024 {
		t.Fatalf("first: want a reference with size and preview, got ref=%v size=%d preview=%d text=%d", first.GetRef(), first.GetSize(), len(first.GetPreview()), len(first.GetText().GetText()))
	}
	if second.GetRef() || second.GetText().GetText() != big || !reqs[1].GetFetched() {
		t.Fatalf("second: want the content inline, got ref=%v text=%d", second.GetRef(), len(second.GetText().GetText()))
	}
}

// Answers it can't apply are errors: enforce refuses the call (onError deny)
// or lets it through unchanged (allow), and records why.
func TestV2InvalidAnswers(t *testing.T) {
	keepOnly := func(n int) func(*pb.RequestMessages) *pb.RequestVerdict { // drops parts after the first n
		return func(q *pb.RequestMessages) *pb.RequestVerdict {
			m := q.GetMessages()[1]
			m.Parts = m.Parts[:n]
			return &pb.RequestVerdict{Messages: []*pb.MessageVerdict{{MessageId: m.GetId(), Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}}}}}
		}
	}
	cases := []struct {
		name   string
		answer func(*pb.RequestMessages) *pb.RequestVerdict
		reason string
	}{
		{"a dropped image", keepOnly(1), "part"},
		{"an altered image", func(q *pb.RequestMessages) *pb.RequestVerdict {
			m := q.GetMessages()[1]
			m.GetParts()[1].GetMedia().Data = []byte("other")
			return &pb.RequestVerdict{Messages: []*pb.MessageVerdict{{MessageId: m.GetId(), Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}}}}}
		}, "media"},
		{"an unknown message", func(*pb.RequestMessages) *pb.RequestVerdict {
			return &pb.RequestVerdict{Messages: []*pb.MessageVerdict{{MessageId: "nope"}}}
		}, "unknown message"},
		{"a second Fetch", func(q *pb.RequestMessages) *pb.RequestVerdict {
			return &pb.RequestVerdict{Fetch: &pb.Fetch{PartIds: []string{q.GetMessages()[0].GetParts()[0].GetId()}}}
		}, "second Fetch"},
		{"a Fetch of a part that isn't there", func(*pb.RequestMessages) *pb.RequestVerdict {
			return &pb.RequestVerdict{Fetch: &pb.Fetch{PartIds: []string{"x/0"}}}
		}, "unknown part"},
	}
	for _, tc := range cases {
		for _, onError := range []string{"deny", "allow"} {
			t.Run(tc.name+" "+onError, func(t *testing.T) {
				f := &interceptortest.Fake{Request: tc.answer}
				var rw rows
				c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second, OnError: onError}, rw.add)
				defer c.Close()
				d := c.DecideRequest(context.Background(), call, parse(t, withHistory))
				if want := map[string]interceptor.Action{"deny": interceptor.Deny, "allow": interceptor.Allow}[onError]; d.Action != want {
					t.Fatalf("decision %v, want %v", d.Action, want)
				}
				got := rw.all()
				if len(got) != 1 || got[0].Action != "error" || got[0].Applied != (onError == "deny") || !strings.Contains(got[0].Reason, tc.reason) {
					t.Fatalf("rows %+v; want one error mentioning %q", got, tc.reason)
				}
			})
		}
	}
}

func respond(text string) *pb.Respond { return &pb.Respond{Text: text} }

// Respond at call start: the decision carries the text; its row waits for
// the observer to say how it was delivered (or that it couldn't be).
func TestV2RespondAtCallStart(t *testing.T) {
	f := &interceptortest.Fake{Start: func(*pb.CallStart) *pb.CallVerdict {
		return &pb.CallVerdict{Action: &pb.CallVerdict_Respond{Respond: respond("no")}, Annotations: []*pb.Annotation{{Key: "rule", Value: "r"}}}
	}}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	d := c.DecideStart(context.Background(), &pb.CallStart{Call: call})
	if d.Action != interceptor.Respond || d.Text != "no" {
		t.Fatalf("decision %+v", d)
	}
	if got := rw.all(); len(got) != 0 {
		t.Fatalf("rows before delivery %+v", got)
	}
	c.Responded("c1", d, "synthesized")
	got := rw.all()
	if len(got) != 1 || got[0].Callback != "call_start" || got[0].Action != "respond" || !got[0].Applied ||
		got[0].RespondMode != "synthesized" || got[0].Mode != "enforce" || got[0].Annotations["rule"] != "r" {
		t.Fatalf("rows %+v", got)
	}
	// Couldn't be delivered: recorded, not applied.
	c.Responded("c1", d, "")
	if got := rw.all(); len(got) != 2 || got[1].Applied || got[1].RespondMode != "" {
		t.Fatalf("rows %+v", got)
	}
}

// Respond at OnRequest: the call's answer; the per-message answers beside it
// are ignored.
func TestV2RespondAtRequest(t *testing.T) {
	f := &interceptortest.Fake{Request: func(q *pb.RequestMessages) *pb.RequestVerdict {
		v := replaceText("I'm a silly rabbit", "tricks")(q)
		v.Respond = respond("Well, tricks are for kids.")
		return v
	}}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	d := c.DecideRequest(context.Background(), call, parse(t, withHistory))
	if d.Action != interceptor.Respond || d.Text != "Well, tricks are for kids." || d.Payload != nil {
		t.Fatalf("decision %+v", d)
	}
	c.Responded("c1", d, "rewritten")
	if got := rw.all(); len(got) != 1 || got[0].Callback != "request" || got[0].Action != "respond" || got[0].RespondMode != "rewritten" || !got[0].Applied {
		t.Fatalf("rows %+v", got)
	}
}

// A Respond with no text is an invalid answer.
func TestV2RespondNeedsText(t *testing.T) {
	f := &interceptortest.Fake{
		Start: func(*pb.CallStart) *pb.CallVerdict {
			return &pb.CallVerdict{Action: &pb.CallVerdict_Respond{Respond: respond("")}}
		},
		Request: func(*pb.RequestMessages) *pb.RequestVerdict { return &pb.RequestVerdict{Respond: respond("")} },
	}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	if d := c.DecideStart(context.Background(), &pb.CallStart{Call: call}); d.Action != interceptor.Deny {
		t.Fatalf("start %+v", d)
	}
	if d := c.DecideRequest(context.Background(), call, parse(t, withHistory)); d.Action != interceptor.Deny {
		t.Fatalf("request %+v", d)
	}
	if got := rw.all(); len(got) != 2 || got[0].Action != "error" || got[1].Action != "error" || !strings.Contains(got[1].Reason, "text") {
		t.Fatalf("rows %+v", got)
	}
}

// Observe: a Respond is recorded, never applied.
func TestV2ObserveRespond(t *testing.T) {
	f := &interceptortest.Fake{
		Start: func(*pb.CallStart) *pb.CallVerdict {
			return &pb.CallVerdict{Action: &pb.CallVerdict_Respond{Respond: respond("a")}}
		},
		Request: func(*pb.RequestMessages) *pb.RequestVerdict { return &pb.RequestVerdict{Respond: respond("b")} },
	}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true}, rw.add)
	c.ObserveStart(&pb.CallStart{Call: call})
	c.ObserveRequest(call, parse(t, withHistory))
	c.Close()
	got := rw.all()
	if len(got) != 2 || got[0].Action != "respond" || got[1].Action != "respond" || got[0].Applied || got[1].Applied || got[1].Mode != "observe" {
		t.Fatalf("rows %+v", got)
	}
}

var bash = convo.ToolCall{ID: "toolu_1", Name: "Bash", Arguments: `{"command":"echo first we bing"}`}

func toolAnswer(a *pb.MessageVerdict) func(*pb.ResponseMessage) *pb.MessageVerdict {
	return func(*pb.ResponseMessage) *pb.MessageVerdict { return a }
}

// OnToolCall gets the call as an assistant message with one tool call part.
func TestV2ToolCallMessage(t *testing.T) {
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	if !c.ToolCalls() {
		t.Fatal("tool calls not decided")
	}
	if d := c.DecideToolCall(context.Background(), call, bash); d.Action != interceptor.Allow {
		t.Fatalf("decision %+v", d)
	}
	got := f.ToolCallsSeen()
	if len(got) != 1 {
		t.Fatalf("%d OnToolCall", len(got))
	}
	m := got[0].GetMessage()
	tc := m.GetParts()[0].GetToolCall()
	if got[0].GetCall().GetCallId() != "c1" || m.GetId() != "toolu_1" || m.GetRole() != pb.Role_ROLE_ASSISTANT || len(m.GetParts()) != 1 ||
		m.GetParts()[0].GetId() != "toolu_1/0" || tc.GetCallId() != "toolu_1" || tc.GetName() != "Bash" || tc.GetArguments() != bash.Arguments ||
		m.GetParts()[0].GetSize() != int64(len(bash.Arguments)) {
		t.Fatalf("message %v", got[0])
	}
}

func replaced(args string) *pb.MessageVerdict {
	return &pb.MessageVerdict{MessageId: "toolu_1", Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: &pb.ConvMessage{
		Id: "toolu_1", Role: pb.Role_ROLE_ASSISTANT, Parts: []*pb.Part{{Id: "toolu_1/0", Kind: &pb.Part_ToolCall{ToolCall: &pb.ToolCall{CallId: "toolu_1", Name: "Bash", Arguments: args}}}},
	}}}}
}

func TestV2ToolCallAnswers(t *testing.T) {
	cases := []struct {
		name   string
		answer *pb.MessageVerdict
		want   interceptor.Decision
		row    string
	}{
		{"accept", &pb.MessageVerdict{}, interceptor.Decision{Action: interceptor.Allow}, ""},
		{"replace", replaced(`{"command":"echo then we bong"}`), interceptor.Decision{Action: interceptor.Replace, Payload: []byte(`{"command":"echo then we bong"}`)}, "replace"},
		{"replace unchanged", replaced(bash.Arguments), interceptor.Decision{Action: interceptor.Allow}, ""},
		{"respond", &pb.MessageVerdict{Action: &pb.MessageVerdict_Respond{Respond: respond("then we bong")}}, interceptor.Decision{Action: interceptor.Respond, Text: "then we bong"}, "respond"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &interceptortest.Fake{Tool: toolAnswer(tc.answer)}
			var rw rows
			c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
			defer c.Close()
			d := c.DecideToolCall(context.Background(), call, bash)
			if d.Action != tc.want.Action || string(d.Payload) != string(tc.want.Payload) || d.Text != tc.want.Text {
				t.Fatalf("decision %+v, want %+v", d, tc.want)
			}
			got := rw.all()
			if tc.row == "" {
				if len(got) != 0 {
					t.Fatalf("rows %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Callback != "tool_call" || got[0].Action != tc.row || !got[0].Applied || got[0].MessageID != "toolu_1" ||
				(tc.row == "respond") != (got[0].RespondMode == "synthesized") {
				t.Fatalf("rows %+v", got)
			}
		})
	}
}

// A custom tool's arguments are a JSON string; a Replace must keep them one.
func TestV2ToolCallCustom(t *testing.T) {
	f := &interceptortest.Fake{Tool: func(m *pb.ResponseMessage) *pb.MessageVerdict {
		v := replaced(`{"not":"a string"}`)
		v.GetReplace().GetMessage().GetParts()[0].GetToolCall().Name = "exec"
		v.GetReplace().GetMessage().GetParts()[0].GetToolCall().CallId = "call_2"
		v.MessageId = "call_2"
		v.GetReplace().GetMessage().Id = "call_2"
		v.GetReplace().GetMessage().GetParts()[0].Id = "call_2/0"
		return v
	}}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	d := c.DecideToolCall(context.Background(), call, convo.ToolCall{ID: "call_2", Name: "exec", Arguments: `"ls"`, Custom: true})
	if d.Action != interceptor.Deny || len(rw.all()) != 1 || !strings.Contains(rw.all()[0].Reason, "string") {
		t.Fatalf("decision %+v rows %+v", d, rw.all())
	}
}

func TestV2ToolCallInvalid(t *testing.T) {
	cases := map[string]*pb.MessageVerdict{
		"not JSON": replaced(`{"command":`),
		"another name": func() *pb.MessageVerdict {
			v := replaced(`{}`)
			v.GetReplace().GetMessage().GetParts()[0].GetToolCall().Name = "Read"
			return v
		}(),
		"no parts":   {Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: &pb.ConvMessage{}}}},
		"empty text": {Action: &pb.MessageVerdict_Respond{Respond: respond("")}},
	}
	for name, answer := range cases {
		for _, onError := range []string{"deny", "allow"} {
			t.Run(name+" "+onError, func(t *testing.T) {
				f := &interceptortest.Fake{Tool: toolAnswer(answer)}
				var rw rows
				c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second, OnError: onError}, rw.add)
				defer c.Close()
				d := c.DecideToolCall(context.Background(), call, bash)
				if want := map[string]interceptor.Action{"deny": interceptor.Deny, "allow": interceptor.Allow}[onError]; d.Action != want {
					t.Fatalf("decision %v, want %v", d.Action, want)
				}
				if got := rw.all(); len(got) != 1 || got[0].Action != "error" || got[0].Callback != "tool_call" || got[0].MessageID != "toolu_1" {
					t.Fatalf("rows %+v", got)
				}
			})
		}
	}
}

// An Interceptor without OnToolCall accepts every call, and AIntercept stops
// holding them.
func TestV2ToolCallUnimplemented(t *testing.T) {
	f := &interceptortest.Fake{NoToolCall: true}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	if d := c.DecideToolCall(context.Background(), call, bash); d.Action != interceptor.Allow {
		t.Fatalf("decision %+v", d)
	}
	if c.ToolCalls() || len(rw.all()) != 0 {
		t.Fatalf("still holding tool calls; rows %+v", rw.all())
	}
}

// Large arguments go by reference, so OnToolCall stays under 4 MiB.
func TestV2ToolCallLarge(t *testing.T) {
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	args := `{"content":"` + strings.Repeat("x", 5<<20) + `"}`
	c.DecideToolCall(context.Background(), call, convo.ToolCall{ID: "t", Name: "Write", Arguments: args})
	got := f.ToolCallsSeen()
	if len(got) != 1 {
		t.Fatal("OnToolCall didn't arrive")
	}
	p := got[0].GetMessage().GetParts()[0]
	if !p.GetRef() || p.GetToolCall().GetArguments() != "" || p.GetSize() != int64(len(args)) || len(p.GetPreview()) != 1024 {
		t.Fatalf("part ref %v size %d preview %d", p.GetRef(), p.GetSize(), len(p.GetPreview()))
	}
}

// The call's record says how a Respond was delivered.
func TestV2RecordRespond(t *testing.T) {
	f := &interceptortest.Fake{Tool: toolAnswer(&pb.MessageVerdict{Action: &pb.MessageVerdict_Respond{Respond: respond("x")}})}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	c.DecideToolCall(context.Background(), call, bash)
	c.Complete(&pb.CallRecord{Call: call}, true)
	c.Close()
	_, _, recs := f.Snapshot()
	if len(recs) != 1 || recs[0].GetRespond() != "synthesized" || recs[0].GetVerdicts()[0].GetRespondMode() != "synthesized" {
		t.Fatalf("records %v", recs)
	}
}

// The deadline covers both rounds; a late Interceptor is onError.
func TestV2Deadline(t *testing.T) {
	f := &interceptortest.Fake{Delay: 200 * time.Millisecond}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 20 * time.Millisecond}, rw.add)
	defer c.Close()
	if d := c.DecideRequest(context.Background(), call, parse(t, withHistory)); d.Action != interceptor.Deny || d.HTTPStatus != 503 {
		t.Fatalf("decision %+v", d)
	}
}

// E5: a 20 MiB history yields an OnRequest well under gRPC's default 4 MiB
// (the fake keeps that limit); so does a call adding many large parts.
func TestV2MessagesStaySmall(t *testing.T) {
	chunk := strings.Repeat("h", 512<<10)
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, `{"role":"user","content":"%d %s"},{"role":"assistant","content":"%d %s"},`, i, chunk, i, chunk)
	}
	b.WriteString(`{"role":"user","content":"now"}]}`)
	if b.Len() < 20<<20 {
		t.Fatalf("history is %d bytes, want 20 MiB", b.Len())
	}

	var many strings.Builder
	many.WriteString(`{"messages":[{"role":"user","content":"go"},{"role":"assistant","content":"ok"},{"role":"user","content":[`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"type":"tool_result","tool_use_id":"t%d","content":"%s"}`, i, strings.Repeat("r", 60<<10))
	}
	many.WriteString(`]}]}`)

	for name, body := range map[string]string{"20 MiB history": b.String(), "100 large tool results": many.String()} {
		t.Run(name, func(t *testing.T) {
			f := &interceptortest.Fake{}
			c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 10 * time.Second}, nil)
			defer c.Close()
			if d := c.DecideRequest(context.Background(), call, parse(t, body)); d.Action != interceptor.Allow {
				t.Fatalf("decision %+v", d)
			}
			_, reqs, _ := f.Snapshot()
			if len(reqs) != 1 {
				t.Fatal("OnRequest didn't arrive")
			}
			if n := proto.Size(reqs[0]); n >= 4<<20 {
				t.Fatalf("OnRequest is %d bytes", n)
			}
		})
	}
}

// Observe: the answer is recorded, never applied; the call's record carries
// it, with the message's id.
func TestV2Observe(t *testing.T) {
	f := &interceptortest.Fake{Request: replaceText("I'm a silly rabbit", "tricks")}
	var rw rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true}, rw.add)
	r := parse(t, withHistory)
	c.ObserveStart(&pb.CallStart{Call: call})
	c.ObserveRequest(call, r)
	c.Complete(&pb.CallRecord{Call: call, Response: &pb.ResponseInfo{Status: 200, InputTokens: 7}}, true)
	c.Close()
	calls, _, recs := f.Snapshot()
	if strings.Join(calls, ",") != "start,request,complete" {
		t.Fatalf("calls %v", calls)
	}
	id := r.IDs("sess-1")[4]
	if got := rw.all(); len(got) != 1 || got[0].Applied || got[0].Mode != "observe" || got[0].Action != "replace" || got[0].MessageID != id {
		t.Fatalf("rows %+v", got)
	}
	rec := recs[0]
	if rec.GetCall().GetCallId() != "c1" || rec.GetResponse().GetInputTokens() != 7 || len(rec.GetVerdicts()) != 1 || rec.GetVerdicts()[0].GetMessageId() != id {
		t.Fatalf("record %v", rec)
	}
}

// A tool the harness adds mid-session arrives as a ToolAdded part naming it;
// a Replace of its message changes the text beside it and must return the
// tool as it came.
func TestV2ToolAdded(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"go"},{"role":"assistant","content":"ok"},` +
		`{"role":"system","content":[{"type":"text","text":"Tools changed."},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__docs__read"}}}]}]}`
	f := &interceptortest.Fake{Request: replaceText("Tools changed.", "Tools moved.")}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	d := c.DecideRequest(context.Background(), call, parse(t, body))
	if d.Action != interceptor.Replace || string(d.Payload) != strings.Replace(body, "Tools changed.", "Tools moved.", 1) {
		t.Fatalf("decision %v %s", d.Action, d.Payload)
	}
	_, reqs, _ := f.Snapshot()
	m := reqs[0].GetMessages()[0]
	if p := m.GetParts()[1]; m.GetRole() != pb.Role_ROLE_SYSTEM || p.GetToolAdded().GetName() != "mcp__docs__read" || p.GetSize() == 0 {
		t.Fatalf("message %v", m)
	}
	// A Replace that changes the tool is refused.
	f.Request = func(q *pb.RequestMessages) *pb.RequestVerdict {
		m := proto.Clone(q.GetMessages()[0]).(*pb.ConvMessage)
		m.GetParts()[1].GetToolAdded().Name = "Other"
		return &pb.RequestVerdict{Messages: []*pb.MessageVerdict{{MessageId: m.GetId(), Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}}}}}
	}
	if d := c.DecideRequest(context.Background(), call, parse(t, body)); d.Action == interceptor.Replace {
		t.Fatalf("a changed tool was applied: %s", d.Payload)
	}
}

// A request with no new messages (Claude Code resuming a thread with an empty
// messages list) gets no OnRequest: there's nothing to decide. OnCallStart
// still fires (the caller's), and the reply still goes to OnReply.
func TestV2NoNewMessagesSkipsOnRequest(t *testing.T) {
	r, ok := convo.Parse("api.anthropic.com", "/v1/messages", []byte(`{"model":"m","thread":{"previous_message_id":"msg_0"},"messages":[]}`))
	if !ok || len(r.New) != 0 || !r.Chained {
		t.Fatalf("parsed %v %+v", ok, r)
	}
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	if d := c.DecideRequest(context.Background(), call, r); d.Action != interceptor.Allow {
		t.Fatalf("decision %+v", d)
	}
	c.ObserveRequest(call, r)
	c.Close()
	if calls, _, _ := f.Snapshot(); len(calls) != 0 {
		t.Fatalf("calls %v: OnRequest for no messages", calls)
	}
	if st := c.Stats(); st.NoNewMessages != 2 {
		t.Fatalf("stats %+v", st)
	}
}
