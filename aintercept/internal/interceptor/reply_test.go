package interceptor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

// OnReply: the model's reply text, before the client sees it.

var piece = convo.TextPiece{ID: "msg_1:0", Released: "The ", Pending: "purple ele", Done: false}

func replyAnswer(v *pb2.MessageVerdict) func(*pb2.ResponseMessage) *pb2.MessageVerdict {
	return func(*pb2.ResponseMessage) *pb2.MessageVerdict { return v }
}

func textReplace(id, text string, keep int32) *pb2.MessageVerdict {
	return &pb2.MessageVerdict{MessageId: id, Keep: keep, Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: &pb2.ConvMessage{
		Id: id, Parts: []*pb2.Part{{Id: id + "/0", Kind: &pb2.Part_Text{Text: &pb2.Text{Text: text}}}}}}}}
}

// The message: one text part, the pending text, with what was released and
// whether the block is done.
func TestV2ReplyMessage(t *testing.T) {
	f := &interceptortest.FakeV2{Reply: replyAnswer(&pb2.MessageVerdict{})}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	if d := c.DecideReply(context.Background(), call, piece); d.Action != interceptor.Allow {
		t.Fatalf("decision %+v", d)
	}
	m := f.RepliesSeen()[0]
	p := m.GetMessage().GetParts()
	if m.GetCall().GetCallId() != "c1" || m.GetMessage().GetId() != "msg_1:0" || m.GetMessage().GetRole() != pb2.Role_ROLE_ASSISTANT ||
		m.GetReleased() != "The " || m.GetDone() || len(p) != 1 || p[0].GetId() != "msg_1:0/0" || p[0].GetText().GetText() != "purple ele" {
		t.Fatalf("OnReply got %v", m)
	}
}

func TestV2ReplyAnswers(t *testing.T) {
	cases := []struct {
		name   string
		answer *pb2.MessageVerdict
		want   interceptor.Decision
		row    string
	}{
		{"accept", &pb2.MessageVerdict{}, interceptor.Decision{Action: interceptor.Allow}, ""},
		{"accept keeping a tail", &pb2.MessageVerdict{Keep: 6}, interceptor.Decision{Action: interceptor.Allow, Keep: 6}, "accept"},
		{"replace", textReplace("msg_1:0", "[x]", 0), interceptor.Decision{Action: interceptor.Replace, Text: "[x]"}, "replace"},
		{"replace keeping a tail", textReplace("msg_1:0", "[x]", 3), interceptor.Decision{Action: interceptor.Replace, Text: "[x]", Keep: 3}, "replace"},
		{"replace unchanged", textReplace("msg_1:0", "purple ele", 0), interceptor.Decision{Action: interceptor.Allow}, ""},
		{"replace with nothing", textReplace("msg_1:0", "", 0), interceptor.Decision{Action: interceptor.Replace}, "replace"},
		{"hold", &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Hold{Hold: &pb2.Hold{}}}, interceptor.Decision{Action: interceptor.Hold}, "hold"},
		{"respond", &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Respond{Respond: respond("then we bong")}}, interceptor.Decision{Action: interceptor.Respond, Text: "then we bong"}, "respond"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &interceptortest.FakeV2{Reply: replyAnswer(tc.answer)}
			var rw rows
			c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
			defer c.Close()
			d := c.DecideReply(context.Background(), call, piece)
			if d.Action != tc.want.Action || d.Text != tc.want.Text || d.Keep != tc.want.Keep {
				t.Fatalf("decision %+v, want %+v", d, tc.want)
			}
			got := rw.all()
			if tc.row == "" {
				if len(got) != 0 {
					t.Fatalf("rows %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Callback != "reply" || got[0].Action != tc.row || !got[0].Applied || got[0].MessageID != "msg_1:0" {
				t.Fatalf("rows %+v", got)
			}
		})
	}
}

// Once the block is done, keep is ignored: nothing can stay held.
func TestV2ReplyKeepIgnoredWhenDone(t *testing.T) {
	f := &interceptortest.FakeV2{Reply: replyAnswer(&pb2.MessageVerdict{Keep: 3})}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	p := piece
	p.Done = true
	if d := c.DecideReply(context.Background(), call, p); d.Action != interceptor.Allow || d.Keep != 0 {
		t.Fatalf("decision %+v", d)
	}
}

// Answers that can't be applied are onError: the call is refused (deny) or
// the text released (allow), and recorded as an error either way.
func TestV2ReplyInvalid(t *testing.T) {
	cases := map[string]*pb2.MessageVerdict{
		"keep past the text":     {Keep: 12},
		"keep mid-character":     {Keep: 1},
		"negative keep":          {Keep: -1},
		"respond without text":   {Action: &pb2.MessageVerdict_Respond{Respond: &pb2.Respond{}}},
		"replace with a call":    {Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: &pb2.ConvMessage{Parts: []*pb2.Part{{Kind: &pb2.Part_ToolCall{ToolCall: &pb2.ToolCall{}}}}}}}},
		"replace with two parts": {Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: &pb2.ConvMessage{Parts: []*pb2.Part{{Kind: &pb2.Part_Text{Text: &pb2.Text{}}}, {Kind: &pb2.Part_Text{Text: &pb2.Text{}}}}}}}},
	}
	p := piece
	p.Pending = "purple elé" // ends in a two-byte character
	for name, v := range cases {
		for _, onError := range []string{"deny", "allow"} {
			t.Run(name+"/"+onError, func(t *testing.T) {
				f := &interceptortest.FakeV2{Reply: replyAnswer(v)}
				var rw rows
				c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second, OnError: onError}, rw.add)
				defer c.Close()
				d := c.DecideReply(context.Background(), call, p)
				want := interceptor.Deny
				if onError == "allow" {
					want = interceptor.Allow
				}
				if got := rw.all(); d.Action != want || len(got) != 1 || got[0].Action != "error" || got[0].Callback != "reply" || got[0].MessageID != "msg_1:0" {
					t.Fatalf("decision %+v, rows %+v", d, got)
				}
			})
		}
	}
}

// A late Interceptor is onError too.
func TestV2ReplyDeadline(t *testing.T) {
	f := &interceptortest.FakeV2{Reply: replyAnswer(&pb2.MessageVerdict{}), Delay: 200 * time.Millisecond}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 20 * time.Millisecond}, nil)
	defer c.Close()
	if d := c.DecideReply(context.Background(), call, piece); d.Action != interceptor.Deny {
		t.Fatalf("decision %+v", d)
	}
}

// Without OnReply, every reply's text is accepted and no longer held.
func TestV2ReplyUnimplemented(t *testing.T) {
	f := &interceptortest.FakeV2{}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, rw.add)
	defer c.Close()
	if !c.Replies() {
		t.Fatal("not holding replies to begin with")
	}
	if d := c.DecideReply(context.Background(), call, piece); d.Action != interceptor.Allow {
		t.Fatalf("decision %+v", d)
	}
	if c.Replies() || len(rw.all()) != 0 {
		t.Fatalf("still holding replies; rows %+v", rw.all())
	}
}

// Observe: OnReply and OnToolCall queued, their answers recorded, never
// applied; an opaque part is announced, with nothing to record.
func TestV2ObserveReply(t *testing.T) {
	f := &interceptortest.FakeV2{
		Reply: func(m *pb2.ResponseMessage) *pb2.MessageVerdict {
			if m.GetMessage().GetParts()[0].GetOpaque() != nil {
				return &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Respond{Respond: respond("ignored")}}
			}
			return textReplace(m.GetMessage().GetId(), strings.ToUpper(m.GetMessage().GetParts()[0].GetText().GetText()), 0)
		},
		Tool: toolAnswer(replaced(`{"command":"pwd"}`)),
	}
	var rw rows
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true}, rw.add)
	c.Opaque(call, "msg_1:0")
	c.ObserveReply(call, piece)
	c.ObserveToolCall(call, bash)
	c.Complete(&pb.CallRecord{Call: call, Response: &pb.ResponseInfo{Status: 200}}, true)
	c.Close()
	calls, _, recs := f.Snapshot()
	if strings.Join(calls, ",") != "reply,reply,tool_call,complete" {
		t.Fatalf("calls %v", calls)
	}
	got := rw.all()
	if len(got) != 2 || got[0].Callback != "reply" || got[0].Action != "replace" || got[0].Applied || got[0].Mode != "observe" ||
		got[1].Callback != "tool_call" || got[1].Action != "replace" || got[1].Applied {
		t.Fatalf("rows %+v", got)
	}
	if vs := recs[0].GetVerdicts(); len(vs) != 2 || vs[0].GetCallback() != "reply" || vs[0].GetMessageId() != "msg_1:0" {
		t.Fatalf("record %v", recs[0])
	}
}
