package interceptor_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
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
	f := &interceptortest.FakeV2{}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
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
	if res.GetId() != ids[3] || res.GetIndex() != 3 || res.GetRole() != pb2.Role_ROLE_TOOL ||
		res.GetParts()[0].GetToolResult().GetText() != "a.txt" || res.GetParts()[0].GetToolResult().GetCallId() != "t1" || res.GetParts()[0].GetId() != ids[3]+"/0" {
		t.Fatalf("tool result %v", res)
	}
	if note.GetId() != ids[4] || note.GetIndex() != 4 || note.GetRole() != pb2.Role_ROLE_USER || len(note.GetParts()) != 2 ||
		note.GetParts()[0].GetText().GetText() != "I'm a silly rabbit" || note.GetParts()[1].GetId() != ids[4]+"/1" {
		t.Fatalf("note %v", note)
	}
	if m := note.GetParts()[1].GetMedia(); m.GetMediaType() != "image/png" || !bytes.Equal(m.GetData(), []byte{0, 1, 2}) || note.GetParts()[1].GetSize() != 3 {
		t.Fatalf("image %v", note.GetParts()[1])
	}
}

// replaceText answers with the message whose text part says from, said to.
func replaceText(from, to string) func(*pb2.RequestMessages) *pb2.RequestVerdict {
	return func(q *pb2.RequestMessages) *pb2.RequestVerdict {
		v := &pb2.RequestVerdict{}
		for _, m := range q.GetMessages() {
			m := proto.Clone(m).(*pb2.ConvMessage)
			changed := false
			for _, p := range m.GetParts() {
				if t := p.GetText(); t != nil && strings.Contains(t.GetText(), from) {
					t.Text = strings.ReplaceAll(t.GetText(), from, to)
					changed = true
				}
			}
			if changed {
				v.Messages = append(v.Messages, &pb2.MessageVerdict{MessageId: m.GetId(),
					Action:      &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: m}},
					Annotations: []*pb2.Annotation{{Key: "rule", Value: "rabbit"}}})
			}
		}
		return v
	}
}

func TestV2ReplaceRewritesTheRequest(t *testing.T) {
	f := &interceptortest.FakeV2{Request: replaceText("I'm a silly rabbit", "tricks")}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
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
	f := &interceptortest.FakeV2{}
	f.Request = func(q *pb2.RequestMessages) *pb2.RequestVerdict {
		p := q.GetMessages()[0].GetParts()[0]
		if !q.GetFetched() {
			return &pb2.RequestVerdict{Fetch: &pb2.Fetch{PartIds: []string{p.GetId()}}}
		}
		return replaceText(big, "short")(q)
	}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
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
	keepOnly := func(n int) func(*pb2.RequestMessages) *pb2.RequestVerdict { // drops parts after the first n
		return func(q *pb2.RequestMessages) *pb2.RequestVerdict {
			m := q.GetMessages()[1]
			m.Parts = m.Parts[:n]
			return &pb2.RequestVerdict{Messages: []*pb2.MessageVerdict{{MessageId: m.GetId(), Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: m}}}}}
		}
	}
	cases := []struct {
		name   string
		answer func(*pb2.RequestMessages) *pb2.RequestVerdict
		reason string
	}{
		{"a dropped image", keepOnly(1), "part"},
		{"an altered image", func(q *pb2.RequestMessages) *pb2.RequestVerdict {
			m := q.GetMessages()[1]
			m.GetParts()[1].GetMedia().Data = []byte("other")
			return &pb2.RequestVerdict{Messages: []*pb2.MessageVerdict{{MessageId: m.GetId(), Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: m}}}}}
		}, "media"},
		{"an unknown message", func(*pb2.RequestMessages) *pb2.RequestVerdict {
			return &pb2.RequestVerdict{Messages: []*pb2.MessageVerdict{{MessageId: "nope"}}}
		}, "unknown message"},
		{"a second Fetch", func(q *pb2.RequestMessages) *pb2.RequestVerdict {
			return &pb2.RequestVerdict{Fetch: &pb2.Fetch{PartIds: []string{q.GetMessages()[0].GetParts()[0].GetId()}}}
		}, "second Fetch"},
		{"a Fetch of a part that isn't there", func(*pb2.RequestMessages) *pb2.RequestVerdict {
			return &pb2.RequestVerdict{Fetch: &pb2.Fetch{PartIds: []string{"x/0"}}}
		}, "unknown part"},
	}
	for _, tc := range cases {
		for _, onError := range []string{"deny", "allow"} {
			t.Run(tc.name+" "+onError, func(t *testing.T) {
				f := &interceptortest.FakeV2{Request: tc.answer}
				var rw rows
				c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second, OnError: onError}, rw.add)
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

func respond(text string) *pb2.Respond { return &pb2.Respond{Text: text} }

// Respond at call start: the decision carries the text; its row waits for
// the observer to say how it was delivered (or that it couldn't be).
func TestV2RespondAtCallStart(t *testing.T) {
	f := &interceptortest.FakeV2{Start: func(*pb2.CallStart) *pb2.CallVerdict {
		return &pb2.CallVerdict{Action: &pb2.CallVerdict_Respond{Respond: respond("no")}, Annotations: []*pb2.Annotation{{Key: "rule", Value: "r"}}}
	}}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
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
	f := &interceptortest.FakeV2{Request: func(q *pb2.RequestMessages) *pb2.RequestVerdict {
		v := replaceText("I'm a silly rabbit", "tricks")(q)
		v.Respond = respond("Well, tricks are for kids.")
		return v
	}}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
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
	f := &interceptortest.FakeV2{
		Start: func(*pb2.CallStart) *pb2.CallVerdict {
			return &pb2.CallVerdict{Action: &pb2.CallVerdict_Respond{Respond: respond("")}}
		},
		Request: func(*pb2.RequestMessages) *pb2.RequestVerdict { return &pb2.RequestVerdict{Respond: respond("")} },
	}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
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
	f := &interceptortest.FakeV2{
		Start: func(*pb2.CallStart) *pb2.CallVerdict {
			return &pb2.CallVerdict{Action: &pb2.CallVerdict_Respond{Respond: respond("a")}}
		},
		Request: func(*pb2.RequestMessages) *pb2.RequestVerdict { return &pb2.RequestVerdict{Respond: respond("b")} },
	}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true}, rw.add)
	c.ObserveStart(&pb.CallStart{Call: call})
	c.ObserveRequest(call, parse(t, withHistory))
	c.Close()
	got := rw.all()
	if len(got) != 2 || got[0].Action != "respond" || got[1].Action != "respond" || got[0].Applied || got[1].Applied || got[1].Mode != "observe" {
		t.Fatalf("rows %+v", got)
	}
}

var bash = convo.ToolCall{ID: "toolu_1", Name: "Bash", Arguments: `{"command":"echo first we bing"}`}

func toolAnswer(a *pb2.MessageVerdict) func(*pb2.ResponseMessage) *pb2.MessageVerdict {
	return func(*pb2.ResponseMessage) *pb2.MessageVerdict { return a }
}

// OnToolCall gets the call as an assistant message with one tool call part.
func TestV2ToolCallMessage(t *testing.T) {
	f := &interceptortest.FakeV2{}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
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
	if got[0].GetCall().GetCallId() != "c1" || m.GetId() != "toolu_1" || m.GetRole() != pb2.Role_ROLE_ASSISTANT || len(m.GetParts()) != 1 ||
		m.GetParts()[0].GetId() != "toolu_1/0" || tc.GetCallId() != "toolu_1" || tc.GetName() != "Bash" || tc.GetArguments() != bash.Arguments ||
		m.GetParts()[0].GetSize() != int64(len(bash.Arguments)) {
		t.Fatalf("message %v", got[0])
	}
}

func replaced(args string) *pb2.MessageVerdict {
	return &pb2.MessageVerdict{MessageId: "toolu_1", Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: &pb2.ConvMessage{
		Id: "toolu_1", Role: pb2.Role_ROLE_ASSISTANT, Parts: []*pb2.Part{{Id: "toolu_1/0", Kind: &pb2.Part_ToolCall{ToolCall: &pb2.ToolCall{CallId: "toolu_1", Name: "Bash", Arguments: args}}}},
	}}}}
}

func TestV2ToolCallAnswers(t *testing.T) {
	cases := []struct {
		name   string
		answer *pb2.MessageVerdict
		want   interceptor.Decision
		row    string
	}{
		{"accept", &pb2.MessageVerdict{}, interceptor.Decision{Action: interceptor.Allow}, ""},
		{"replace", replaced(`{"command":"echo then we bong"}`), interceptor.Decision{Action: interceptor.Replace, Payload: []byte(`{"command":"echo then we bong"}`)}, "replace"},
		{"replace unchanged", replaced(bash.Arguments), interceptor.Decision{Action: interceptor.Allow}, ""},
		{"respond", &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Respond{Respond: respond("then we bong")}}, interceptor.Decision{Action: interceptor.Respond, Text: "then we bong"}, "respond"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &interceptortest.FakeV2{Tool: toolAnswer(tc.answer)}
			var rw rows
			c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
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
	f := &interceptortest.FakeV2{Tool: func(m *pb2.ResponseMessage) *pb2.MessageVerdict {
		v := replaced(`{"not":"a string"}`)
		v.GetReplace().GetMessage().GetParts()[0].GetToolCall().Name = "exec"
		v.GetReplace().GetMessage().GetParts()[0].GetToolCall().CallId = "call_2"
		v.MessageId = "call_2"
		v.GetReplace().GetMessage().Id = "call_2"
		v.GetReplace().GetMessage().GetParts()[0].Id = "call_2/0"
		return v
	}}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	d := c.DecideToolCall(context.Background(), call, convo.ToolCall{ID: "call_2", Name: "exec", Arguments: `"ls"`, Custom: true})
	if d.Action != interceptor.Deny || len(rw.all()) != 1 || !strings.Contains(rw.all()[0].Reason, "string") {
		t.Fatalf("decision %+v rows %+v", d, rw.all())
	}
}

func TestV2ToolCallInvalid(t *testing.T) {
	cases := map[string]*pb2.MessageVerdict{
		"not JSON": replaced(`{"command":`),
		"another name": func() *pb2.MessageVerdict {
			v := replaced(`{}`)
			v.GetReplace().GetMessage().GetParts()[0].GetToolCall().Name = "Read"
			return v
		}(),
		"no parts":   {Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: &pb2.ConvMessage{}}}},
		"empty text": {Action: &pb2.MessageVerdict_Respond{Respond: respond("")}},
	}
	for name, answer := range cases {
		for _, onError := range []string{"deny", "allow"} {
			t.Run(name+" "+onError, func(t *testing.T) {
				f := &interceptortest.FakeV2{Tool: toolAnswer(answer)}
				var rw rows
				c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second, OnError: onError}, rw.add)
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
	f := &interceptortest.FakeV2{NoToolCall: true}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
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
	f := &interceptortest.FakeV2{}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
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
	f := &interceptortest.FakeV2{Tool: toolAnswer(&pb2.MessageVerdict{Action: &pb2.MessageVerdict_Respond{Respond: respond("x")}})}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
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
	f := &interceptortest.FakeV2{Delay: 200 * time.Millisecond}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 20 * time.Millisecond}, rw.add)
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
			f := &interceptortest.FakeV2{}
			c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 10 * time.Second}, nil)
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
	f := &interceptortest.FakeV2{Request: replaceText("I'm a silly rabbit", "tricks")}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true}, rw.add)
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

// v2's Call, CallStart and CallRecord are v1's, field for field: converted,
// nothing is lost.
func TestV2CallIsV1s(t *testing.T) {
	full := &pb.CallStart{
		Call: &pb.Call{CallId: "c", Mode: pb.Mode_MODE_ENFORCE, Transport: pb.Transport_TRANSPORT_WEBSOCKET, Host: "h", Method: "GET", Path: "/p", StartUnixNano: 9,
			Identity: &pb.Identity{KeyHash: "kh", KeyHint: "hint", Ip: "1.2.3.4", AccountId: "a", OrgId: "o", DeviceId: "d", SessionId: "s", Email: "e@example.com"},
			Client:   &pb.Client{Product: "codex", UserAgent: "ua", Headers: map[string]string{"originator": "x"}}},
		Headers: map[string]string{"h": "v"},
		Request: &pb.RequestInfo{Model: "m", Effort: "high", Stream: true, MaxTokens: 5, ToolsAvailable: []string{"t"}, McpToolsAvailable: []string{"s/t"}, Prewarm: true, Attachments: 2, RequestBytes: 100},
	}
	v2 := interceptor.StartV2(full)
	if v2.GetCall().GetIdentity().GetEmail() != "e@example.com" || v2.GetRequest().GetMcpToolsAvailable()[0] != "s/t" || v2.GetCall().GetMode() != pb2.Mode_MODE_ENFORCE {
		t.Fatalf("v2 %v", v2)
	}
	b, _ := proto.Marshal(v2)
	back := &pb.CallStart{}
	if err := proto.Unmarshal(b, back); err != nil || !proto.Equal(back, full) {
		t.Fatalf("round trip lost fields: %v", back)
	}
}
