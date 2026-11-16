// redact is a minimal example Interceptor (docs/INTERCEPTOR.md): it replaces
// "silly clown" with "[redacted]" wherever it appears in a call's content,
// in both directions, refuses calls that ask for the model
// "forbidden-model", and logs each call's start and record. It also answers
// two phrases in what the client sends: "I'm a silly rabbit" is rewritten to
// ask the model for "Well, tricks are for kids.", and "first we bing" for
// "then we bong". Everything else goes through unchanged.
//
// It shows the whole contract: OnCallStart decides from the parsed request
// (Deny or Allow), OnMessage returns Replace when it changes a message (Allow
// otherwise), and OnCallComplete receives the normalized record. Run it, point the observer
// at it (INTERCEPTOR_ENDPOINT=<host>:9100, INTERCEPTOR_CONTENT=true), and put
// a host in enforce scope to see prompts redacted before they reach the
// provider. In observe mode the same verdicts are only recorded.
//
// Matching is per message: a phrase split across two streamed events (two
// deltas of the model's text) isn't seen. A real policy would track state per
// call (Call.call_id) where that matters. The rabbit is matched in the newest
// thing the user said (newest), not the whole body: clients resend the whole
// conversation, and a rabbit earlier in it is history.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/grpc"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
)

var phrase = regexp.MustCompile(`(?i)silly clown`)

// The rabbit, with a straight or curly apostrophe (raw, or JSON-escaped), or none.
const apostrophe = `(?:'|’|\\u0027|\\u2019)?`

var tricks = ask("Well, tricks are for kids.")

// answers: when the newest thing the user said matches, the request is
// rewritten to ask the model for the answer instead.
var answers = []struct {
	name   string
	said   *regexp.Regexp
	answer []byte
}{
	{"silly rabbit", regexp.MustCompile(`(?i)i` + apostrophe + `m a silly rabbit`), tricks},
	{"bing", regexp.MustCompile(`(?i)first,? we bing`), ask("then we bong")},
}

// ask is the instruction that makes the model reply with exactly this. No
// quotes or backslashes in reply: it goes inside JSON strings as it is.
func ask(reply string) []byte {
	return []byte("Reply with exactly this sentence and nothing else: " + reply)
}

type server struct {
	pb.UnimplementedInterceptorServer
}

func (server) OnCallStart(_ context.Context, s *pb.CallStart) (*pb.Verdict, error) {
	c := s.GetCall()
	slog.Info("call start", "call", c.GetCallId(), "mode", c.GetMode().String(), "host", c.GetHost(), "path", c.GetPath(),
		"product", c.GetClient().GetProduct(), "model", s.GetRequest().GetModel(), "key", c.GetIdentity().GetKeyHint())
	if s.GetRequest().GetModel() == "forbidden-model" {
		return &pb.Verdict{Action: &pb.Verdict_Deny{Deny: &pb.Deny{Reason: "forbidden-model isn't approved for use here."}}}, nil
	}
	return &pb.Verdict{Action: &pb.Verdict_Allow{Allow: &pb.Allow{}}}, nil
}

func (server) OnMessage(_ context.Context, m *pb.Message) (*pb.Verdict, error) {
	in := m.GetPayload()
	var notes []*pb.Annotation
	if m.GetDirection() == pb.Direction_DIRECTION_CLIENT_TO_SERVER {
		said, ok := newest(in)
		if !ok {
			said = string(in) // not a request we know: the whole message
		}
		for _, a := range answers {
			if !a.said.MatchString(said) {
				continue
			}
			in = a.said.ReplaceAll(in, a.answer)
			notes = append(notes, &pb.Annotation{Key: "answered", Value: a.name})
			slog.Info("answered", "rule", a.name, "call", m.GetCall().GetCallId(), "kind", m.GetKind().String(),
				"applied", m.GetCall().GetMode() == pb.Mode_MODE_ENFORCE)
		}
	}
	n := len(phrase.FindAllIndex(in, -1))
	if n == 0 && notes == nil {
		return &pb.Verdict{}, nil // unset action: Allow
	}
	out := phrase.ReplaceAll(in, []byte("[redacted]"))
	if n > 0 {
		slog.Info("redacted", "call", m.GetCall().GetCallId(), "direction", m.GetDirection().String(),
			"kind", m.GetKind().String(), "matches", n, "applied", m.GetCall().GetMode() == pb.Mode_MODE_ENFORCE)
		notes = append(notes, &pb.Annotation{Key: "redacted", Value: strconv.Itoa(n)})
	}
	return &pb.Verdict{Action: &pb.Verdict_Replace{Replace: &pb.Replace{Payload: out}}, Annotations: notes}, nil
}

func (server) OnCallComplete(_ context.Context, r *pb.CallRecord) (*pb.Ack, error) {
	resp := r.GetResponse()
	slog.Info("call complete", "call", r.GetCall().GetCallId(), "ws_turn", r.GetWsTurn(), "status", resp.GetStatus(),
		"model", resp.GetModel(), "in", resp.GetInputTokens(), "out", resp.GetOutputTokens(),
		"duration_ms", resp.GetDurationMs(), "verdicts", len(r.GetVerdicts()))
	return &pb.Ack{}, nil
}

func main() {
	listen := flag.String("listen", envOr("REDACT_LISTEN", ":9100"), "gRPC listen address")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	g := newServer()
	slog.Info("redact interceptor listening", "addr", *listen)
	if err := g.Serve(l); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

// newServer takes messages up to 33 MiB: the observer sends request bodies up
// to 32 MiB, and gRPC's default 4 MiB limit would fail a long session's.
// newest is the newest thing the user said in a request body we know: the last
// message or input item when it's the user's (the Anthropic and OpenAI APIs,
// Claude Code, Codex over HTTP or its WebSocket, ChatGPT on the web), or
// claude.ai's prompt. ok is false for any other body.
func newest(payload []byte) (said string, ok bool) {
	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
		Prompt   *string           `json:"prompt"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return "", false
	}
	switch {
	case body.Prompt != nil:
		return *body.Prompt, true
	case len(body.Messages) > 0:
		return userText(body.Messages[len(body.Messages)-1]), true
	case len(body.Input) > 0:
		var s string
		if json.Unmarshal(body.Input, &s) == nil {
			return s, true
		}
		var items []json.RawMessage
		if json.Unmarshal(body.Input, &items) == nil {
			if len(items) == 0 {
				return "", true
			}
			return userText(items[len(items)-1]), true
		}
	}
	return "", false
}

// userText is a message's text when the user sent it: its content as a string,
// its text blocks (not tool results), or ChatGPT's content parts.
func userText(raw json.RawMessage) string {
	var m struct {
		Role   string `json:"role"`
		Author struct {
			Role string `json:"role"`
		} `json:"author"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil || (m.Role != "user" && m.Author.Role != "user") {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var texts []string
	var blocks []struct{ Type, Text string }
	var chatgpt struct{ Parts []json.RawMessage }
	if json.Unmarshal(m.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" || b.Type == "input_text" {
				texts = append(texts, b.Text)
			}
		}
	} else if json.Unmarshal(m.Content, &chatgpt) == nil {
		for _, p := range chatgpt.Parts {
			if json.Unmarshal(p, &s) == nil {
				texts = append(texts, s)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func newServer() *grpc.Server {
	g := grpc.NewServer(grpc.MaxRecvMsgSize(33 << 20))
	pb.RegisterInterceptorServer(g, server{})
	return g
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
