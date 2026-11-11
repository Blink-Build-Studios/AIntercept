// redact is a minimal example Interceptor (docs/INTERCEPTOR.md): it replaces
// "silly clown" with "[redacted]" wherever it appears in a call's content,
// in both directions, refuses calls that ask for the model
// "forbidden-model", and logs each call's start and record.
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
// call (Call.call_id) where that matters.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"

	"google.golang.org/grpc"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
)

var phrase = regexp.MustCompile(`(?i)silly clown`)

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
	n := len(phrase.FindAllIndex(m.GetPayload(), -1))
	if n == 0 {
		return &pb.Verdict{}, nil // unset action: Allow
	}
	out := phrase.ReplaceAll(m.GetPayload(), []byte("[redacted]"))
	slog.Info("redacted", "call", m.GetCall().GetCallId(), "direction", m.GetDirection().String(),
		"kind", m.GetKind().String(), "matches", n, "applied", m.GetCall().GetMode() == pb.Mode_MODE_ENFORCE)
	return &pb.Verdict{
		Action:      &pb.Verdict_Replace{Replace: &pb.Replace{Payload: out}},
		Annotations: []*pb.Annotation{{Key: "redacted", Value: strconv.Itoa(n)}},
	}, nil
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
	g := grpc.NewServer()
	pb.RegisterInterceptorServer(g, server{})
	slog.Info("redact interceptor listening", "addr", *listen)
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
