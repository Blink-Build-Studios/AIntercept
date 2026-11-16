// rabbit is a minimal Interceptor on contract v2 (docs/INTERCEPTOR_V2.md):
// when a new message the user sends says "I'm a silly rabbit", it replaces
// that text with an instruction asking the model for "Well, tricks are for
// kids.". Everything else is accepted.
//
// v2 hands it only the messages each call adds, in one shape for Claude
// Code, Codex (HTTP or WebSocket) and the OpenAI APIs, so there's no body to
// parse and no history to skip: a rabbit earlier in the conversation is
// never seen again. AIntercept puts the replaced message back in the
// request, in the provider's format.
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

// The rabbit, with a straight or curly apostrophe, or none.
var rabbit = regexp.MustCompile(`(?i)i['’]?m a silly rabbit`)

const tricks = "Reply with exactly this sentence and nothing else: Well, tricks are for kids."

type server struct {
	pb.UnimplementedInterceptorServer
}

func (server) OnCallStart(_ context.Context, s *pb.CallStart) (*pb.CallVerdict, error) {
	c := s.GetCall()
	slog.Info("call start", "call", c.GetCallId(), "mode", c.GetMode().String(), "host", c.GetHost(), "path", c.GetPath(),
		"product", c.GetClient().GetProduct(), "model", s.GetRequest().GetModel())
	return &pb.CallVerdict{}, nil
}

func (server) OnRequest(_ context.Context, r *pb.RequestMessages) (*pb.RequestVerdict, error) {
	v := &pb.RequestVerdict{}
	for _, m := range r.GetMessages() {
		if m.GetRole() != pb.Role_ROLE_USER {
			continue
		}
		hit := false
		for _, p := range m.GetParts() {
			if t := p.GetText(); t != nil && rabbit.MatchString(t.GetText()) {
				t.Text = rabbit.ReplaceAllString(t.GetText(), tricks)
				hit = true
			}
		}
		if hit {
			// The message as it was, with its text changed: every part comes back.
			v.Messages = append(v.Messages, &pb.MessageVerdict{
				MessageId:   m.GetId(),
				Action:      &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: m}},
				Annotations: []*pb.Annotation{{Key: "answered", Value: "silly rabbit"}},
			})
		}
	}
	slog.Info("request", "call", r.GetCall().GetCallId(), "format", r.GetFormat(), "new", len(r.GetMessages()),
		"of", r.GetTotalMessages(), "replaced", len(v.GetMessages()))
	return v, nil
}

func (server) OnCallComplete(_ context.Context, r *pb.CallRecord) (*pb.Ack, error) {
	resp := r.GetResponse()
	slog.Info("call complete", "call", r.GetCall().GetCallId(), "ws_turn", r.GetWsTurn(), "status", resp.GetStatus(),
		"model", resp.GetModel(), "in", resp.GetInputTokens(), "out", resp.GetOutputTokens(), "verdicts", len(r.GetVerdicts()))
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
