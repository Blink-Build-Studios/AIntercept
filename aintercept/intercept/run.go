package intercept

import (
	"context"
	"expvar"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// MaxMessageBytes is the receive limit Run sets: a fetched part (NeedFull)
// can be up to 32 MiB.
const MaxMessageBytes = 33 << 20

// Run serves the Interceptor built from rules, hooks and watches until
// SIGINT or SIGTERM, then stops gracefully. It listens on INTERCEPT_LISTEN
// (default :9100), with the receive limit NeedFull needs, gRPC health checks,
// a structured log line per decision (other than a plain Accept), and, when
// INTERCEPT_METRICS_LISTEN is set, decision counts at /debug/vars there.
func Run(items ...Item) error {
	s := New(items...)
	s.log = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	addr := os.Getenv("INTERCEPT_LISTEN")
	if addr == "" {
		addr = ":9100"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if m := os.Getenv("INTERCEPT_METRICS_LISTEN"); m != "" {
		go func() { s.log.Error("intercept: metrics", "err", http.ListenAndServe(m, expvar.Handler())) }()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	g := s.GRPC()
	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()
	s.log.Info("intercept: serving", "addr", lis.Addr().String(), "rules", len(s.rules), "watches", len(s.watches))
	return g.Serve(lis)
}

// GRPC is a gRPC server serving s, with health checks and the receive limit
// NeedFull needs.
func (s *Server) GRPC(opts ...grpc.ServerOption) *grpc.Server {
	g := grpc.NewServer(append([]grpc.ServerOption{grpc.MaxRecvMsgSize(MaxMessageBytes)}, opts...)...)
	pb.RegisterInterceptorServer(g, s)
	healthpb.RegisterHealthServer(g, health.NewServer())
	return g
}

// stats counts decisions by callback and answer, published as the expvar
// "intercept_decisions".
type stats struct{ m *expvar.Map }

var (
	statsOnce sync.Once
	decisions *expvar.Map
)

func newStats() *stats {
	statsOnce.Do(func() { decisions = expvar.NewMap("intercept_decisions") })
	return &stats{m: decisions}
}

func (s *stats) add(callback, answer string) { s.m.Add(callback+" "+answer, 1) }
