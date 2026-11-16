package interceptortest

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
)

// FakeV2 is an in-memory v2 Interceptor whose answers the test sets. Its gRPC
// server keeps the default 4 MiB receive limit.
type FakeV2 struct {
	pb2.UnimplementedInterceptorServer
	mu       sync.Mutex
	Start    func(*pb2.CallStart) *pb2.CallVerdict
	Request  func(*pb2.RequestMessages) *pb2.RequestVerdict
	Delay    time.Duration
	Calls    []string // "start", "request", "complete"
	Requests []*pb2.RequestMessages
	Records  []*pb2.CallRecord
}

func (f *FakeV2) OnCallStart(_ context.Context, s *pb2.CallStart) (*pb2.CallVerdict, error) {
	time.Sleep(f.Delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "start")
	if f.Start != nil {
		return f.Start(s), nil
	}
	return &pb2.CallVerdict{}, nil
}

func (f *FakeV2) OnRequest(_ context.Context, r *pb2.RequestMessages) (*pb2.RequestVerdict, error) {
	time.Sleep(f.Delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "request")
	f.Requests = append(f.Requests, r)
	if f.Request != nil {
		return f.Request(r), nil
	}
	return &pb2.RequestVerdict{}, nil
}

func (f *FakeV2) OnCallComplete(_ context.Context, r *pb2.CallRecord) (*pb2.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "complete")
	f.Records = append(f.Records, r)
	return &pb2.Ack{}, nil
}

// Snapshot returns the callbacks received so far, the requests and the records.
func (f *FakeV2) Snapshot() ([]string, []*pb2.RequestMessages, []*pb2.CallRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...), append([]*pb2.RequestMessages(nil), f.Requests...), append([]*pb2.CallRecord(nil), f.Records...)
}

// NewClientV2 starts f in memory and returns a client using contract v2.
func NewClientV2(t *testing.T, f *FakeV2, cfg interceptor.Config, record func(string, events.Verdict)) *interceptor.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb2.RegisterInterceptorServer(g, f)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	cfg.Endpoint = "passthrough:///fake"
	cfg.Contract = "v2"
	cfg.DialOptions = []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})}
	c, err := interceptor.New(cfg, record)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
