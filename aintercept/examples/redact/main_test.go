package main

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
)

func onMessage(t *testing.T, dir pb.Direction, payload string) *pb.Verdict {
	t.Helper()
	v, err := server{}.OnMessage(context.Background(), &pb.Message{
		Call: &pb.Call{CallId: "c", Mode: pb.Mode_MODE_ENFORCE}, Direction: dir, Payload: []byte(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRabbits(t *testing.T) {
	up := pb.Direction_DIRECTION_CLIENT_TO_SERVER
	for _, tc := range []struct {
		name, payload, want string // want: "allow", or the replaced payload
	}{
		{"nothing to see", `{"messages":[{"role":"user","content":"hello"}]}`, "allow"},
		{"silly", `{"content":"I'm a silly rabbit!"}`,
			`{"content":"Reply with exactly this sentence and nothing else: Well, tricks are for kids.!"}`},
		{"silly, curly apostrophe", "I’M A SILLY RABBIT", string(tricks)},
		{"silly, JSON-escaped apostrophe", `{"t":"I’m a silly rabbit"}`, `{"t":"` + string(tricks) + `"}`},
		{"silly, no apostrophe", "im a silly rabbit", string(tricks)},
		{"silly and a clown", "I'm a silly rabbit, silly clown", string(tricks) + ", [redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := onMessage(t, up, tc.payload)
			switch {
			case tc.want == "allow":
				if v.GetAction() != nil {
					t.Errorf("got %v, want allow", v.GetAction())
				}
			default:
				if got := string(v.GetReplace().GetPayload()); got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// "First we bing" gets "then we bong", the same way.
func TestBing(t *testing.T) {
	up := pb.Direction_DIRECTION_CLIENT_TO_SERVER
	for _, said := range []string{"first we bing", "First, we BING!"} {
		v := onMessage(t, up, `{"prompt":"`+said+`"}`)
		if got := string(v.GetReplace().GetPayload()); !strings.Contains(got, "Reply with exactly this sentence and nothing else: then we bong") {
			t.Errorf("%q: got %q, want it to ask for then we bong", said, got)
		}
	}
	if v := onMessage(t, up, `{"prompt":"we bing first"}`); v.GetAction() != nil {
		t.Errorf("got %v, want allow", v.GetAction())
	}
}

// Only the newest thing the user said counts: clients resend the whole
// conversation, and a rabbit earlier in it is history.
func TestRabbitsInTheNewestMessageOnly(t *testing.T) {
	up := pb.Direction_DIRECTION_CLIENT_TO_SERVER
	for _, tc := range []struct {
		name, payload string
		rewrite       bool
	}{
		{"Claude Code, a new prompt", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},` +
			`{"role":"user","content":[{"type":"text","text":"I'm a silly rabbit"},{"type":"text","text":"<system-reminder>x</system-reminder>"}]}]}`, true},
		{"Claude Code, said earlier", `{"messages":[{"role":"user","content":"I'm a silly rabbit"},{"role":"assistant","content":"no"},` +
			`{"role":"user","content":"what's the time?"}]}`, false},
		{"Claude Code, a tool result after it", `{"messages":[{"role":"user","content":"I'm a silly rabbit"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`, false},
		{"Codex, a new turn", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]}]}`, true},
		{"Codex, said earlier", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]},` +
			`{"type":"function_call_output","call_id":"c","output":"ok"}]}`, false},
		{"Codex WebSocket", `{"type":"response.create","model":"m","previous_response_id":"r",` +
			`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]}]}`, true},
		{"ChatGPT", `{"action":"next","messages":[{"id":"m","author":{"role":"user"},` +
			`"content":{"content_type":"text","parts":["I'm a silly rabbit"]}}]}`, true},
		{"claude.ai", `{"prompt":"I'm a silly rabbit","timezone":"UTC"}`, true},
		{"claude.ai, something else", `{"prompt":"hello","note":"I'm a silly rabbit"}`, false},
		{"not a request we know", `{"note":"I'm a silly rabbit"}`, true},
		{"not JSON", `I'm a silly rabbit`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := onMessage(t, up, tc.payload).GetReplace() != nil; got != tc.rewrite {
				t.Errorf("rewritten %v, want %v", got, tc.rewrite)
			}
		})
	}
}

// What the model says is never a rabbit: only what the client sends is.
func TestRabbitsOnlyFromTheClient(t *testing.T) {
	if v := onMessage(t, pb.Direction_DIRECTION_SERVER_TO_CLIENT, "I'm a silly rabbit"); v.GetAction() != nil {
		t.Errorf("got %v, want allow", v.GetAction())
	}
}

// The observer sends request bodies up to 32 MiB (a long Claude Code session's
// is several); gRPC's default 4 MiB receive limit would fail them.
func TestTakesALongSessionsBody(t *testing.T) {
	l := bufconn.Listen(1 << 20)
	g := newServer()
	go func() { _ = g.Serve(l) }()
	defer g.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(33<<20))) // as the observer's client
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := strings.Repeat("x", 30<<20) + "I'm a silly rabbit"
	v, err := pb.NewInterceptorClient(conn).OnMessage(context.Background(), &pb.Message{
		Call: &pb.Call{CallId: "c"}, Direction: pb.Direction_DIRECTION_CLIENT_TO_SERVER, Payload: []byte(body),
	})
	if err != nil {
		t.Fatalf("a 30 MiB body: %v", err)
	}
	if !strings.HasSuffix(string(v.GetReplace().GetPayload()), string(tricks)) {
		t.Errorf("got %v, want the silly rabbit rewritten", v.GetAction())
	}
}
