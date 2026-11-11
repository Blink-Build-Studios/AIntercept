// Package interceptor is the observer's client for a customer's Interceptor
// service (interceptor/v1): the control-plane hook. See docs/INTERCEPTOR.md.
//
// Two ways to call it, matching the two modes:
//
//   - Observe* and Complete queue the callback and return at once. Workers
//     deliver them in order per call; a full queue drops and counts, so a slow
//     or unavailable Interceptor never reaches the request path. Verdicts are
//     recorded, never applied.
//   - Decide calls OnCallStart / OnMessage synchronously, within the deadline,
//     for the enforcing filter. A failure or a missed deadline becomes Deny
//     (fail closed) unless OnError is "allow".
//
// Every verdict, in both modes, is reported through the record callback (a
// verdict row in AIntercept's own stream) and attached to the call's
// CallRecord.
package interceptor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Config configures the client. An empty Endpoint means no Interceptor.
type Config struct {
	// Endpoint is the Interceptor's gRPC address, host:port.
	Endpoint string
	// Content enables OnMessage: request and response bodies go to the
	// Interceptor. Independent of what AIntercept records.
	Content bool
	// Deadline bounds each enforce-mode callback (default 50 ms).
	Deadline time.Duration
	// ObserveTimeout bounds each observe-mode and OnCallComplete call
	// (default 5 s).
	ObserveTimeout time.Duration
	// OnError is what enforce does when the Interceptor fails or misses the
	// deadline: "deny" (default, fail closed) or "allow".
	OnError string
	// QueueSize bounds queued observe callbacks per worker (default 1024);
	// Workers is the number of delivery goroutines (default 8).
	QueueSize int
	Workers   int
	// TLS: off by default (plaintext, for a sidecar or in-cluster service).
	// With TLS on, CAFile verifies the server (system roots when empty),
	// CertFile/KeyFile present a client certificate (mutual TLS), and
	// ServerName overrides the name verified.
	TLS        bool
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
	// Token, when set, is sent on every call as "authorization: Bearer <token>".
	Token string
	// DialOptions are added to the connection's (tests use an in-memory dialer).
	DialOptions []grpc.DialOption
}

// Action is a verdict's action.
type Action string

const (
	Allow   Action = "allow"
	Replace Action = "replace"
	Drop    Action = "drop"
	Deny    Action = "deny"
)

// Decision is an enforce-mode verdict, ready to apply.
type Decision struct {
	Action Action
	// Payload replaces the message's (Replace).
	Payload []byte
	// Reason and HTTPStatus describe a Deny (status 0 means 403).
	Reason     string
	HTTPStatus int
}

// Client calls one Interceptor.
type Client struct {
	cfg    Config
	conn   *grpc.ClientConn
	api    pb.InterceptorClient
	record func(callID string, v events.Verdict)

	queues []chan job
	wg     sync.WaitGroup

	mu       sync.Mutex
	verdicts map[string]*callVerdicts

	queued, dropped, delivered, failed, decided, decideFailed atomic.Uint64
}

type job struct {
	callID string
	start  *pb.CallStart
	msg    *pb.Message
	rec    *pb.CallRecord
	// final marks the record that ends the call (its verdicts can go).
	final bool
}

type callVerdicts struct {
	list []*pb.RecordedVerdict
	seen time.Time
}

// New connects to the Interceptor (lazily: gRPC dials on first use and
// reconnects on its own). record receives every verdict; it must not block.
func New(cfg Config, record func(callID string, v events.Verdict)) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("interceptor: no endpoint")
	}
	if cfg.Deadline <= 0 {
		cfg.Deadline = 50 * time.Millisecond
	}
	if cfg.ObserveTimeout <= 0 {
		cfg.ObserveTimeout = 5 * time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}
	switch cfg.OnError {
	case "":
		cfg.OnError = "deny"
	case "deny", "allow":
	default:
		return nil, fmt.Errorf("interceptor: on-error must be deny or allow, not %q", cfg.OnError)
	}
	creds, err := transportCredentials(cfg)
	if err != nil {
		return nil, err
	}
	// Round-robin across every address the endpoint resolves to (a headless
	// Service with dns:///…), rather than gRPC's default of the first one.
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),
	}
	conn, err := grpc.NewClient(cfg.Endpoint, append(opts, cfg.DialOptions...)...)
	if err != nil {
		return nil, fmt.Errorf("interceptor: %w", err)
	}
	if record == nil {
		record = func(string, events.Verdict) {}
	}
	c := &Client{
		cfg: cfg, conn: conn, api: pb.NewInterceptorClient(conn), record: record,
		verdicts: map[string]*callVerdicts{},
	}
	c.queues = make([]chan job, cfg.Workers)
	for i := range c.queues {
		c.queues[i] = make(chan job, cfg.QueueSize)
		c.wg.Add(1)
		go c.work(c.queues[i])
	}
	return c, nil
}

func transportCredentials(cfg Config) (credentials.TransportCredentials, error) {
	if !cfg.TLS {
		return insecure.NewCredentials(), nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("interceptor: CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("interceptor: CA file has no certificates")
		}
		tc.RootCAs = pool
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("interceptor: client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return credentials.NewTLS(tc), nil
}

// Content reports whether OnMessage is enabled.
func (c *Client) Content() bool { return c != nil && c.cfg.Content }

// Close drains the queues (bounded by ObserveTimeout per call) and closes the
// connection.
func (c *Client) Close() {
	for _, q := range c.queues {
		close(q)
	}
	c.wg.Wait()
	c.conn.Close()
}

func (c *Client) ctx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, d)
	if c.cfg.Token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.cfg.Token)
	}
	return ctx, cancel
}

// ── Observe ──────────────────────────────────────────────────────────────────

// ObserveStart queues OnCallStart (observe mode).
func (c *Client) ObserveStart(s *pb.CallStart) {
	c.enqueue(job{callID: s.GetCall().GetCallId(), start: s})
}

// ObserveMessage queues OnMessage (observe mode). No-op without content.
func (c *Client) ObserveMessage(m *pb.Message) {
	if c.Content() {
		c.enqueue(job{callID: m.GetCall().GetCallId(), msg: m})
	}
}

// Complete queues OnCallComplete (both modes). final marks the call's last
// record: the verdicts recorded for the call are attached and released.
func (c *Client) Complete(r *pb.CallRecord, final bool) {
	c.enqueue(job{callID: r.GetCall().GetCallId(), rec: r, final: final})
}

func (c *Client) enqueue(j job) {
	h := fnv.New32a()
	h.Write([]byte(j.callID))
	q := c.queues[int(h.Sum32()%uint32(len(c.queues)))]
	select {
	case q <- j:
		c.queued.Add(1)
	default:
		if n := c.dropped.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("interceptor queue full: dropping observe callbacks (traffic unaffected)", "total_dropped", n)
		}
	}
}

// work delivers one queue's callbacks, in order.
func (c *Client) work(q chan job) {
	defer c.wg.Done()
	for j := range q {
		c.deliver(j)
	}
}

func (c *Client) deliver(j job) {
	ctx, cancel := c.ctx(context.Background(), c.cfg.ObserveTimeout)
	defer cancel()
	var err error
	switch {
	case j.start != nil:
		t := time.Now()
		var v *pb.Verdict
		if v, err = c.api.OnCallStart(ctx, j.start); err == nil {
			c.note(j.callID, "call_start", "observe", v, false, time.Since(t))
		}
	case j.msg != nil:
		t := time.Now()
		var v *pb.Verdict
		if v, err = c.api.OnMessage(ctx, j.msg); err == nil {
			c.note(j.callID, "message", "observe", v, false, time.Since(t))
		}
	case j.rec != nil:
		j.rec.Verdicts = c.verdictsFor(j.callID, j.final)
		_, err = c.api.OnCallComplete(ctx, j.rec)
	}
	if err != nil {
		if n := c.failed.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("interceptor callback failed (observe: traffic unaffected)", "err", err, "total_failed", n)
		}
		return
	}
	c.delivered.Add(1)
}

// ── Enforce ──────────────────────────────────────────────────────────────────

// DecideStart calls OnCallStart and waits for the verdict (enforce mode).
func (c *Client) DecideStart(ctx context.Context, s *pb.CallStart) Decision {
	return c.decide(ctx, s.GetCall().GetCallId(), "call_start", func(ctx context.Context) (*pb.Verdict, error) {
		return c.api.OnCallStart(ctx, s)
	})
}

// DecideMessage calls OnMessage and waits for the verdict (enforce mode).
func (c *Client) DecideMessage(ctx context.Context, m *pb.Message) Decision {
	return c.decide(ctx, m.GetCall().GetCallId(), "message", func(ctx context.Context) (*pb.Verdict, error) {
		return c.api.OnMessage(ctx, m)
	})
}

func (c *Client) decide(parent context.Context, callID, callback string, call func(context.Context) (*pb.Verdict, error)) Decision {
	ctx, cancel := c.ctx(parent, c.cfg.Deadline)
	defer cancel()
	t := time.Now()
	v, err := call(ctx)
	took := time.Since(t)
	if err != nil {
		c.decideFailed.Add(1)
		applied := c.cfg.OnError == "deny"
		c.store(callID, events.Verdict{
			Callback: callback, Action: "error", Applied: applied, Mode: "enforce",
			Reason: err.Error(), LatencyMS: took.Milliseconds(),
		})
		if applied {
			return Decision{Action: Deny, Reason: "interceptor unavailable", HTTPStatus: 503}
		}
		return Decision{Action: Allow}
	}
	c.decided.Add(1)
	d := decisionOf(v, callback)
	c.note(callID, callback, "enforce", v, true, took)
	return d
}

func decisionOf(v *pb.Verdict, callback string) Decision {
	switch a := v.GetAction().(type) {
	case *pb.Verdict_Replace:
		if callback == "message" {
			return Decision{Action: Replace, Payload: a.Replace.GetPayload()}
		}
	case *pb.Verdict_Drop:
		if callback == "message" {
			return Decision{Action: Drop}
		}
	case *pb.Verdict_Deny:
		return Decision{Action: Deny, Reason: a.Deny.GetReason(), HTTPStatus: int(a.Deny.GetHttpStatus())}
	}
	return Decision{Action: Allow}
}

// ── Recording ────────────────────────────────────────────────────────────────

// note records a verdict the Interceptor returned.
func (c *Client) note(callID, callback, mode string, v *pb.Verdict, applied bool, took time.Duration) {
	d := decisionOf(v, callback)
	ev := events.Verdict{
		Callback: callback, Action: string(d.Action), Applied: applied, Mode: mode,
		Reason: d.Reason, LatencyMS: took.Milliseconds(),
	}
	if len(v.GetAnnotations()) > 0 {
		ev.Annotations = make(map[string]string, len(v.GetAnnotations()))
		for _, a := range v.GetAnnotations() {
			ev.Annotations[a.GetKey()] = a.GetValue()
		}
	}
	if d.Action == Allow && len(ev.Annotations) == 0 {
		return // an unannotated allow is the default; not worth a row
	}
	c.store(callID, ev)
}

// store reports a verdict and keeps it for the call's CallRecord.
func (c *Client) store(callID string, v events.Verdict) {
	c.record(callID, v)
	rv := &pb.RecordedVerdict{Callback: v.Callback, Action: v.Action, Applied: v.Applied, Reason: v.Reason}
	for k, val := range v.Annotations {
		rv.Annotations = append(rv.Annotations, &pb.Annotation{Key: k, Value: val})
	}
	now := time.Now()
	c.mu.Lock()
	cv := c.verdicts[callID]
	if cv == nil {
		cv = &callVerdicts{}
		c.verdicts[callID] = cv
		c.sweepLocked(now)
	}
	cv.list = append(cv.list, rv)
	cv.seen = now
	c.mu.Unlock()
}

// verdictsFor returns the call's verdicts so far; final releases them.
func (c *Client) verdictsFor(callID string, final bool) []*pb.RecordedVerdict {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv := c.verdicts[callID]
	if cv == nil {
		return nil
	}
	if final {
		delete(c.verdicts, callID)
	}
	return cv.list
}

// verdictTTL bounds how long a call's verdicts wait for its final record (a
// call whose complete row never comes, e.g. a dropped event).
const verdictTTL = 15 * time.Minute

func (c *Client) sweepLocked(now time.Time) {
	if len(c.verdicts)%256 != 0 {
		return
	}
	for id, cv := range c.verdicts {
		if now.Sub(cv.seen) > verdictTTL {
			delete(c.verdicts, id)
		}
	}
}

// Stats are the client's counters, for the observer's /stats.
type Stats struct {
	Endpoint     string `json:"endpoint"`
	Content      bool   `json:"content"`
	Queued       uint64 `json:"queued"`
	Dropped      uint64 `json:"dropped"`
	Delivered    uint64 `json:"delivered"`
	Failed       uint64 `json:"failed"`
	Decided      uint64 `json:"decided"`
	DecideFailed uint64 `json:"decide_failed"`
}

// Stats returns the counters.
func (c *Client) Stats() Stats {
	return Stats{
		Endpoint: c.cfg.Endpoint, Content: c.cfg.Content,
		Queued: c.queued.Load(), Dropped: c.dropped.Load(), Delivered: c.delivered.Load(),
		Failed: c.failed.Load(), Decided: c.decided.Load(), DecideFailed: c.decideFailed.Load(),
	}
}
