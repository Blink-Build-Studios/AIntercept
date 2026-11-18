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

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
)

// Fake is an in-memory Interceptor whose verdicts the test sets.
type Fake struct {
	pb.UnimplementedInterceptorServer
	mu       sync.Mutex
	Start    func(*pb.CallStart) *pb.Verdict
	Message  func(*pb.Message) *pb.Verdict
	Delay    time.Duration
	Block    chan struct{}
	Calls    []string // "start", "message:<seq>", "complete"
	Records  []*pb.CallRecord
	Messages []*pb.Message
	// Auth is each call's "authorization" metadata, in order ("" when absent).
	Auth []string
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

func (f *Fake) wait() {
	if f.Block != nil {
		<-f.Block
	}
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
}

func (f *Fake) OnCallStart(ctx context.Context, s *pb.CallStart) (*pb.Verdict, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "start")
	if f.Start != nil {
		return f.Start(s), nil
	}
	return &pb.Verdict{}, nil
}

func (f *Fake) OnMessage(ctx context.Context, m *pb.Message) (*pb.Verdict, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "message")
	f.Messages = append(f.Messages, m)
	if f.Message != nil {
		return f.Message(m), nil
	}
	return &pb.Verdict{}, nil
}

func (f *Fake) OnCallComplete(ctx context.Context, r *pb.CallRecord) (*pb.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth(ctx)
	f.Calls = append(f.Calls, "complete")
	f.Records = append(f.Records, r)
	return &pb.Ack{}, nil
}

// Snapshot returns the callbacks received so far, and the records.
func (f *Fake) Snapshot() ([]string, []*pb.CallRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...), append([]*pb.CallRecord(nil), f.Records...)
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
