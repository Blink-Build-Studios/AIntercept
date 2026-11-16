// rabbit is a minimal Interceptor on contract v2 (docs/INTERCEPTOR_V2.md).
// When the newest message the user sends says "I'm a silly rabbit", it
// answers for the model: "Well, tricks are for kids." When it says "first we
// bing", the answer is "then we bong". Everything else is accepted.
//
// v2 hands it only the messages each call adds, in one shape for Claude
// Code, Codex (HTTP or WebSocket) and the OpenAI APIs, so there's no body to
// parse and no history to skip: a rabbit earlier in the conversation is
// never seen again. A Respond reaches the client as the model's reply, in
// the provider's format: synthesized when the client keeps the conversation,
// or, when the provider does, by asking the model for exactly that text.
//
// Each tool call the model makes comes to OnToolCall before the client sees
// it: one whose arguments say "first we bing" is answered "then we bong"
// instead (the reply ends there), and "I'm a silly rabbit" in the arguments
// is replaced with "Well, tricks are for kids.".
//
// It keeps gRPC's default 4 MiB receive limit: v2's messages stay small
// whatever the conversation holds (large parts arrive by reference).
//
// Run it and point an observer at it (INTERCEPTOR_ENDPOINT=<host>:9100,
// INTERCEPTOR_CONTRACT=v2, INTERCEPTOR_CONTENT=true).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"regexp"

	"google.golang.org/grpc"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// The rules: what the user says (a straight or curly apostrophe, or none),
// and the answer.
var (
	rabbit = regexp.MustCompile(`(?i)i['’]?m a silly rabbit`)
	bing   = regexp.MustCompile(`(?i)first we bing`)
)

const (
	tricks = "Well, tricks are for kids."
	bong   = "then we bong"
)

type server struct {
	pb.UnimplementedInterceptorServer
}

func (server) OnCallStart(_ context.Context, s *pb.CallStart) (*pb.CallVerdict, error) {
	c := s.GetCall()
	slog.Info("call start", "call", c.GetCallId(), "mode", c.GetMode().String(), "host", c.GetHost(), "path", c.GetPath(),
		"product", c.GetClient().GetProduct(), "model", s.GetRequest().GetModel())
	return &pb.CallVerdict{}, nil
}

// answer is the reply a text gets ("" for none).
func answer(s string) string {
	switch {
	case rabbit.MatchString(s):
		return tricks
	case bing.MatchString(s):
		return bong
	}
	return ""
}

func (server) OnRequest(_ context.Context, r *pb.RequestMessages) (*pb.RequestVerdict, error) {
	v := &pb.RequestVerdict{}
	msgs := r.GetMessages()
	for i := len(msgs) - 1; i >= 0; i-- { // the newest user message
		if msgs[i].GetRole() != pb.Role_ROLE_USER {
			continue
		}
		for _, p := range msgs[i].GetParts() {
			if a := answer(p.GetText().GetText()); a != "" {
				v.Respond = &pb.Respond{Text: a}
				v.Annotations = []*pb.Annotation{{Key: "answered", Value: a}}
			}
		}
		break
	}
	slog.Info("request", "call", r.GetCall().GetCallId(), "format", r.GetFormat(), "new", len(msgs),
		"of", r.GetTotalMessages(), "respond", v.GetRespond().GetText())
	return v, nil
}

func (server) OnToolCall(_ context.Context, r *pb.ResponseMessage) (*pb.MessageVerdict, error) {
	m := r.GetMessage()
	tc := m.GetParts()[0].GetToolCall()
	v := &pb.MessageVerdict{MessageId: m.GetId()}
	switch args := tc.GetArguments(); {
	case bing.MatchString(args):
		v.Action = &pb.MessageVerdict_Respond{Respond: &pb.Respond{Text: bong}}
	case rabbit.MatchString(args):
		// The call as it was, with its arguments changed (still valid JSON:
		// the replacement has no quotes or backslashes).
		tc.Arguments = rabbit.ReplaceAllString(args, tricks)
		v.Action = &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}}
	}
	slog.Info("tool call", "call", r.GetCall().GetCallId(), "tool", tc.GetName(), "answered", v.GetAction() != nil)
	return v, nil
}

func (server) OnCallComplete(_ context.Context, r *pb.CallRecord) (*pb.Ack, error) {
	resp := r.GetResponse()
	slog.Info("call complete", "call", r.GetCall().GetCallId(), "ws_turn", r.GetWsTurn(), "status", resp.GetStatus(),
		"model", resp.GetModel(), "in", resp.GetInputTokens(), "out", resp.GetOutputTokens(), "verdicts", len(r.GetVerdicts()),
		"respond", r.GetRespond())
	return &pb.Ack{}, nil
}

func main() {
	listen := flag.String("listen", envOr("RABBIT_LISTEN", ":9100"), "gRPC listen address")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	g := grpc.NewServer() // gRPC's default 4 MiB receive limit
	pb.RegisterInterceptorServer(g, server{})
	slog.Info("rabbit interceptor (v2) listening", "addr", *listen)
	if err := g.Serve(l); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
