// Package interceptor is the observer's client for a customer's Interceptor
// service: the control-plane hook. See docs/INTERCEPTOR.md. A deployment
// speaks one contract: interceptor/v1 (the default), or interceptor/v2, which
// hands over each call's new messages instead of bodies and events (v2.go).
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
// With v2, ObserveStart, DecideStart and Complete speak v2 (the same Call),
// ObserveRequest / DecideRequest replace the Message callbacks, and
// DecideToolCall decides each tool call in a reply.
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
	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Config configures the client. An empty Endpoint means no Interceptor.
type Config struct {
	// Endpoint is the Interceptor's gRPC address, host:port.
	Endpoint string
	// Contract is the interface the Interceptor implements: "v1" (default) or
	// "v2".
	Contract string
	// Content enables the content callbacks: OnMessage (v1: request and
	// response bodies), OnRequest (v2: each call's new messages). Independent
	// of what AIntercept records.
	Content bool
	// InlineLimit (v2) is the size over which a part's content goes by
	// reference (default convo.DefaultInlineLimit, 64 KiB).
	InlineLimit int
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
	// Respond (v2): the client gets Text as the model's reply.
	Respond Action = "respond"
	// Hold (v2, reply text): nothing is released yet.
	Hold Action = "hold"
)

// Decision is an enforce-mode verdict, ready to apply.
type Decision struct {
	Action Action
	// Payload replaces the message's (Replace); for a tool call, its
	// arguments.
	Payload []byte
	// Reason and HTTPStatus describe a Deny (status 0 means 403).
	Reason     string
	HTTPStatus int
	// Text is a Respond's reply, or reply text's replacement.
	Text string
	// Keep (reply text, Allow or Replace): the pending text's last Keep
	// bytes stay held.
	Keep int
	// pending is a call's Respond row, recorded by Responded once the
	// observer knows how it was delivered.
	pending *events.Verdict
}

// Client calls one Interceptor.
type Client struct {
	cfg    Config
	conn   *grpc.ClientConn
	api    pb.InterceptorClient
	api2   pb2.InterceptorClient // contract v2
	record func(callID string, v events.Verdict)

	queues []chan job
	wg     sync.WaitGroup

	mu       sync.Mutex
	verdicts map[string]*callVerdicts
	// unparsedWhy counts the unparsed requests by reason.
	unparsedWhy map[string]uint64

	queued, dropped, delivered, failed, decided, decideFailed, unparsed atomic.Uint64
	// noNew counts requests with no new messages: no OnRequest (v2).
	noNew atomic.Uint64
	// noToolCall, noReply: the Interceptor doesn't implement OnToolCall,
	// OnReply (v2).
	noToolCall, noReply atomic.Bool
}

type job struct {
	callID string
	start  *pb.CallStart
	msg    *pb.Message
	req    *Request  // v2: OnRequest
	reply  *replyJob // v2: OnReply or OnToolCall
	rec    *pb.CallRecord
	// final marks the record that ends the call (its verdicts can go).
	final bool
}

type callVerdicts struct {
	list []events.Verdict
	seen time.Time
}

// New connects to the Interceptor (lazily: gRPC dials on first use and
// reconnects on its own). record receives every verdict; it must not block.
// MaxMessageBytes is the largest callback or verdict either side has to take:
// a request body up to the enforce cap (32 MiB) with room for the rest of the
// message. An Interceptor's gRPC server needs the same receive limit.
const MaxMessageBytes = 33 << 20

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
	switch cfg.Contract {
	case "":
		cfg.Contract = "v1"
	case "v1", "v2":
	default:
		return nil, fmt.Errorf("interceptor: contract must be v1 or v2, not %q", cfg.Contract)
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
	// A Replace verdict carries a whole body back (up to the 32 MiB enforce cap):
	// above gRPC's default 4 MiB receive limit.
	// Round-robin across every address the endpoint resolves to (a headless
	// Service with dns:///…), rather than gRPC's default of the first one.
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes)),
	}
	conn, err := grpc.NewClient(cfg.Endpoint, append(opts, cfg.DialOptions...)...)
	if err != nil {
		return nil, fmt.Errorf("interceptor: %w", err)
	}
	if record == nil {
		record = func(string, events.Verdict) {}
	}
	c := &Client{
		cfg: cfg, conn: conn, api: pb.NewInterceptorClient(conn), api2: pb2.NewInterceptorClient(conn), record: record,
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

// Content reports whether the content callbacks are enabled.
func (c *Client) Content() bool { return c != nil && c.cfg.Content }

// V2 reports whether the Interceptor implements contract v2.
func (c *Client) V2() bool { return c != nil && c.cfg.Contract == "v2" }

// Parser parses requests for OnRequest, with the deployment's inline limit.
func (c *Client) Parser() convo.Parser { return convo.Parser{InlineLimit: c.cfg.InlineLimit} }

// Unparsed counts a request (or client WebSocket message) v2 couldn't read:
// it went upstream untouched, without OnRequest.
func (c *Client) Unparsed(reason string) {
	c.unparsed.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unparsedWhy == nil {
		c.unparsedWhy = map[string]uint64{}
	}
	if _, ok := c.unparsedWhy[reason]; ok || len(c.unparsedWhy) < 32 {
		c.unparsedWhy[reason]++
	}
}

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

// ObserveMessage queues OnMessage (observe mode). No-op without content, and
// with v2.
func (c *Client) ObserveMessage(m *pb.Message) {
	if c.Content() && !c.V2() {
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
	case j.start != nil && c.V2():
		t := time.Now()
		var v *pb2.CallVerdict
		if v, err = c.api2.OnCallStart(ctx, StartV2(j.start)); err == nil {
			c.noteStart2(j.callID, "observe", v, false, time.Since(t))
		}
	case j.req != nil:
		err = c.onRequestObserved(ctx, j.req)
	case j.reply != nil:
		err = c.onReplyObserved(ctx, j.callID, j.reply)
	case j.rec != nil && c.V2():
		rec := convert(j.rec, &pb2.CallRecord{})
		vs := c.verdictsFor(j.callID, j.final)
		rec.Verdicts, rec.Respond = recordedV2(vs), respondMode(vs)
		_, err = c.api2.OnCallComplete(ctx, rec)
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
		j.rec.Verdicts = recordedV1(c.verdictsFor(j.callID, j.final))
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

// DecideStart calls OnCallStart and waits for the verdict (enforce mode). A
// v2 Respond is the caller's to deliver, and to record with Responded.
func (c *Client) DecideStart(ctx context.Context, s *pb.CallStart) Decision {
	if c.V2() {
		return c.decideStart2(ctx, s)
	}
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
		return c.fail(callID, callback, "", err, took)
	}
	c.decided.Add(1)
	d := decisionOf(v, callback)
	c.note(callID, callback, "enforce", v, true, took)
	return d
}

// fail records an enforce-mode failure and applies onError.
func (c *Client) fail(callID, callback, messageID string, err error, took time.Duration) Decision {
	c.decideFailed.Add(1)
	applied := c.cfg.OnError == "deny"
	c.store(callID, events.Verdict{
		Callback: callback, Action: "error", Applied: applied, Mode: "enforce",
		Reason: err.Error(), LatencyMS: took.Milliseconds(), MessageID: messageID,
	})
	if applied {
		return Decision{Action: Deny, Reason: "interceptor unavailable", HTTPStatus: 503}
	}
	return Decision{Action: Allow}
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
	now := time.Now()
	c.mu.Lock()
	cv := c.verdicts[callID]
	if cv == nil {
		cv = &callVerdicts{}
		c.verdicts[callID] = cv
		c.sweepLocked(now)
	}
	cv.list = append(cv.list, v)
	cv.seen = now
	c.mu.Unlock()
}

// verdictsFor returns the call's verdicts so far; final releases them.
func (c *Client) verdictsFor(callID string, final bool) []events.Verdict {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv := c.verdicts[callID]
	if cv == nil {
		return nil
	}
	if final || c.V2() { // v2: each record has the answers since the call's previous one
		delete(c.verdicts, callID)
	}
	return cv.list
}

func recordedV1(vs []events.Verdict) []*pb.RecordedVerdict {
	var out []*pb.RecordedVerdict
	for _, v := range vs {
		rv := &pb.RecordedVerdict{Callback: v.Callback, Action: v.Action, Applied: v.Applied, Reason: v.Reason}
		for k, val := range v.Annotations {
			rv.Annotations = append(rv.Annotations, &pb.Annotation{Key: k, Value: val})
		}
		out = append(out, rv)
	}
	return out
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
	Contract     string `json:"contract"`
	Content      bool   `json:"content"`
	Queued       uint64 `json:"queued"`
	Dropped      uint64 `json:"dropped"`
	Delivered    uint64 `json:"delivered"`
	Failed       uint64 `json:"failed"`
	Decided      uint64 `json:"decided"`
	DecideFailed uint64 `json:"decide_failed"`
	// Unparsed (v2): requests that went upstream untouched, without
	// OnRequest, and replies that couldn't be read for OnReply and
	// OnToolCall (observe), and why.
	Unparsed        uint64            `json:"unparsed"`
	UnparsedReasons map[string]uint64 `json:"unparsed_reasons,omitempty"`
	// NoNewMessages (v2): requests read whole that add no message (a
	// chained request resuming a turn), so had no OnRequest.
	NoNewMessages uint64 `json:"no_new_messages"`
}

// Stats returns the counters.
func (c *Client) Stats() Stats {
	return Stats{
		Endpoint: c.cfg.Endpoint, Contract: c.cfg.Contract, Content: c.cfg.Content,
		Queued: c.queued.Load(), Dropped: c.dropped.Load(), Delivered: c.delivered.Load(),
		Failed: c.failed.Load(), Decided: c.decided.Load(), DecideFailed: c.decideFailed.Load(),
		Unparsed: c.unparsed.Load(), UnparsedReasons: c.unparsedReasons(), NoNewMessages: c.noNew.Load(),
	}
}

func (c *Client) unparsedReasons() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.unparsedWhy) == 0 {
		return nil
	}
	out := make(map[string]uint64, len(c.unparsedWhy))
	for k, v := range c.unparsedWhy {
		out[k] = v
	}
	return out
}
