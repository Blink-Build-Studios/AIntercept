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

// Respond is step 3: until then it's Accept, recorded as respond_unsupported.
func TestV2RespondIsAcceptForNow(t *testing.T) {
	f := &interceptortest.FakeV2{
		Start: func(*pb2.CallStart) *pb2.CallVerdict {
			return &pb2.CallVerdict{Action: &pb2.CallVerdict_Respond{Respond: &pb2.Respond{Text: "no"}}}
		},
		Request: func(*pb2.RequestMessages) *pb2.RequestVerdict {
			return &pb2.RequestVerdict{Respond: &pb2.Respond{Text: "no"}}
		},
	}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	if d := c.DecideStart(context.Background(), &pb.CallStart{Call: call}); d.Action != interceptor.Allow {
		t.Fatalf("start: %v", d.Action)
	}
	if d := c.DecideRequest(context.Background(), call, parse(t, withHistory)); d.Action != interceptor.Allow {
		t.Fatalf("request: %v", d.Action)
	}
	got := rw.all()
	if len(got) != 2 || got[0].Callback != "call_start" || got[1].Callback != "request" ||
		got[0].Action != "respond_unsupported" || got[1].Action != "respond_unsupported" || got[1].Applied {
		t.Fatalf("rows %+v", got)
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
