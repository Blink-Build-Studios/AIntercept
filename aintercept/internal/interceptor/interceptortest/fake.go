// Package interceptortest is an in-memory Interceptor for tests.
package interceptortest

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
)

// Fake is an in-memory Interceptor whose answers the test sets. Its gRPC
// server keeps the default 4 MiB receive limit.
type Fake struct {
	pb.UnimplementedInterceptorServer
	mu    sync.Mutex
	Start func(*pb.CallStart) *pb.CallVerdict
	// Turn answers OnTurnStart; nil leaves it unimplemented, as in an
	// Interceptor that doesn't have it.
	Turn    func(*pb.TurnStart) *pb.CallVerdict
	Request func(*pb.RequestMessages) *pb.RequestVerdict
	Tool    func(*pb.ResponseMessage) *pb.MessageVerdict
	// Reply answers OnReply; nil leaves it unimplemented, as in an
	// Interceptor that doesn't have it.
	Reply func(*pb.ResponseMessage) *pb.MessageVerdict
	// NoToolCall: OnToolCall is unimplemented, as in an Interceptor that
	// doesn't have it.
	NoToolCall bool
	Delay      time.Duration
	// Block, when set, holds every callback until it's closed.
	Block     chan struct{}
	Calls     []string // "start", "turn", "request", "reply", "tool_call", "complete"
	Turns     []*pb.TurnStart
	Requests  []*pb.RequestMessages
	ToolCalls []*pb.ResponseMessage
	Replies   []*pb.ResponseMessage
	Records   []*pb.CallRecord
	// Auth is each call's "authorization" metadata, in order ("" when absent).
	Auth []string
}

func (f *Fake) wait() {
	if f.Block != nil {
		<-f.Block
	}
	time.Sleep(f.Delay)
}

// auth records the call's authorization metadata. Called with f.mu held.
func (f *Fake) auth(ctx context.Context) {
	v := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if a := md.Get("authorization"); len(a) > 0 {
			v = a[0]
		}
	}
	f.Auth = append(f.Auth, v)
}

// Auths returns the authorization metadata of the calls so far.
func (f *Fake) Auths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Auth...)
}

func (f *Fake) OnCallStart(ctx context.Context, s *pb.CallStart) (*pb.CallVerdict, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "start")
	if f.Start != nil {
		return f.Start(s), nil
	}
	return &pb.CallVerdict{}, nil
}

func (f *Fake) OnTurnStart(ctx context.Context, t *pb.TurnStart) (*pb.CallVerdict, error) {
	if f.Turn == nil {
		return f.UnimplementedInterceptorServer.OnTurnStart(ctx, t)
	}
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "turn")
	f.Turns = append(f.Turns, t)
	return f.Turn(t), nil
}

// TurnsSeen returns the OnTurnStart messages received so far.
func (f *Fake) TurnsSeen() []*pb.TurnStart {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.TurnStart(nil), f.Turns...)
}

func (f *Fake) OnRequest(ctx context.Context, r *pb.RequestMessages) (*pb.RequestVerdict, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "request")
	f.Requests = append(f.Requests, r)
	if f.Request != nil {
		return f.Request(r), nil
	}
	return &pb.RequestVerdict{}, nil
}

func (f *Fake) OnToolCall(ctx context.Context, m *pb.ResponseMessage) (*pb.MessageVerdict, error) {
	if f.NoToolCall {
		return f.UnimplementedInterceptorServer.OnToolCall(ctx, m)
	}
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "tool_call")
	f.ToolCalls = append(f.ToolCalls, m)
	if f.Tool != nil {
		return f.Tool(m), nil
	}
	return &pb.MessageVerdict{}, nil
}

func (f *Fake) OnReply(ctx context.Context, m *pb.ResponseMessage) (*pb.MessageVerdict, error) {
	if f.Reply == nil {
		return f.UnimplementedInterceptorServer.OnReply(ctx, m)
	}
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "reply")
	f.Replies = append(f.Replies, m)
	return f.Reply(m), nil
}

// RepliesSeen returns the OnReply messages received so far.
func (f *Fake) RepliesSeen() []*pb.ResponseMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.ResponseMessage(nil), f.Replies...)
}

// ToolCallsSeen returns the OnToolCall messages received so far.
func (f *Fake) ToolCallsSeen() []*pb.ResponseMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.ResponseMessage(nil), f.ToolCalls...)
}

func (f *Fake) OnCallComplete(ctx context.Context, r *pb.CallRecord) (*pb.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "complete")
	f.Records = append(f.Records, r)
	return &pb.Ack{}, nil
}

// Snapshot returns the callbacks received so far, the requests and the records.
func (f *Fake) Snapshot() ([]string, []*pb.RequestMessages, []*pb.CallRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...), append([]*pb.RequestMessages(nil), f.Requests...), append([]*pb.CallRecord(nil), f.Records...)
}

// NewClient starts f in memory and returns a client connected to it.
func NewClient(t *testing.T, f *Fake, cfg interceptor.Config, record func(string, events.Verdict)) *interceptor.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb.RegisterInterceptorServer(g, f)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	cfg.Endpoint = "passthrough:///fake"
	cfg.DialOptions = []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})}
	c, err := interceptor.New(cfg, record)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
