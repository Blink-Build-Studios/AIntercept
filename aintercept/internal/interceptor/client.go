// Package interceptor is the observer's client for a customer's Interceptor
// service: the control-plane hook, contract interceptor/v2. See
// docs/INTERCEPTOR.md. The content callbacks (each call's new messages, the
// reply's text and tool calls) are in v2.go.
//
// Two ways to call it, matching the two modes:
//
//   - Observe* and Complete queue the callback and return at once. Workers
//     deliver them in order per call; a full queue drops and counts, so a slow
//     or unavailable Interceptor never reaches the request path. Verdicts are
//     recorded, never applied.
//   - Decide* call OnCallStart, OnRequest, OnReply and OnToolCall
//     synchronously, within the deadline, for the enforcing filter. A failure
//     or a missed deadline becomes Deny (fail closed) unless OnError is
//     "allow".
//
// Every verdict, in both modes, is reported through the record callback (a
// verdict row in AIntercept's own stream) and attached to the call's
// CallRecord.
//
// Identity: a call's identity is resolved once and carried by every one of
// its callbacks. ObserveCall and DecideCall start a call. When its
// credential's lookup is still in flight, DecideCall waits for it (bounded);
// ObserveCall never waits: it holds the call's callbacks in a buffer of the
// call's own, before the shared queues, until the lookup ends or the wait has
// passed, then queues them in order. Workers never wait on identity.
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
)

// Config configures the client. An empty Endpoint means no Interceptor.
type Config struct {
	// Endpoint is the Interceptor's gRPC address, host:port.
	Endpoint string
	// Content enables the content callbacks: OnRequest (each call's new
	// messages), OnReply and OnToolCall (the reply's text and tool calls).
	// Independent of what AIntercept records.
	Content bool
	// Turns sends each WebSocket request to OnTurnStart without content (with
	// content it always goes). An enforced WebSocket whose turns are decided
	// has its client messages re-framed: compression is off, and a client
	// message over 16 MiB closes the connection.
	Turns bool
	// InlineLimit is the size over which a part's content goes by
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
	// TokenFile is the same, read from a file that something else keeps
	// current: a Kubernetes projected service-account token, which the kubelet
	// rewrites before it expires. It's read at New (which fails if it can't be),
	// then again at most every TokenFileRefresh (default 30 s); a read that fails
	// or finds the file empty keeps the last token. Token or TokenFile, not both.
	TokenFile        string
	TokenFileRefresh time.Duration
	// DialOptions are added to the connection's (tests use an in-memory dialer).
	DialOptions []grpc.DialOption
}

// Action is a verdict's action.
type Action string

const (
	Allow   Action = "allow"
	Replace Action = "replace"
	// Deny refuses the call: the Interceptor failed (onError deny), or the
	// call can't be evaluated.
	Deny Action = "deny"
	// Respond: the client gets Text as the model's reply.
	Respond Action = "respond"
	// Refuse: the client gets an error (HTTPStatus, ErrorType, and Reason as
	// its message), written as the call's API writes its own: an Error
	// answer.
	Refuse Action = "refuse"
	// Hold (reply text): nothing is released yet.
	Hold Action = "hold"
)

// Decision is an enforce-mode verdict, ready to apply.
type Decision struct {
	Action Action
	// Payload is a Replace's rewritten request body; for a tool call, its
	// arguments.
	Payload []byte
	// Reason and HTTPStatus describe a Deny (status 0 means 403) or a
	// Refuse; ErrorType is a Refuse's error type ("": the API's usual one).
	Reason     string
	HTTPStatus int
	ErrorType  string
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
	record func(callID string, v events.Verdict)
	// token is TokenFile's current contents (nil without one).
	token *tokenFile

	queues []chan job
	wg     sync.WaitGroup

	// held are the calls whose callbacks wait for their identity (holding
	// counts them); idents each started call's identity, stamped on its
	// callbacks as they're delivered (until its final record).
	hmu      sync.Mutex
	held     map[string][]job
	holding  atomic.Int32
	idents   map[string]*callIdentity
	awaiting sync.WaitGroup
	now      func() time.Time // the clock idents age by

	mu       sync.Mutex
	verdicts map[string]*callVerdicts
	// unparsedWhy counts the unparsed requests by reason.
	unparsedWhy map[string]uint64

	queued, dropped, delivered, failed, decided, decideFailed, unparsed, heldCalls atomic.Uint64
	// noNew counts requests with no new messages: no OnRequest.
	noNew atomic.Uint64
	// noToolCall, noReply: the Interceptor doesn't implement OnToolCall,
	// OnReply.
	noToolCall, noReply atomic.Bool
	// noTurnUntil: the Interceptor answered OnTurnStart UNIMPLEMENTED; it isn't
	// asked again until then (unix ns), so one old replica in a rolling deploy
	// can't turn it off for good.
	noTurnUntil atomic.Int64
}

type job struct {
	callID string
	start  *pb.CallStart
	turn   *turnJob  // OnTurnStart
	req    *Request  // OnRequest
	reply  *replyJob // OnReply or OnToolCall
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
	switch cfg.OnError {
	case "":
		cfg.OnError = "deny"
	case "deny", "allow":
	default:
		return nil, fmt.Errorf("interceptor: on-error must be deny or allow, not %q", cfg.OnError)
	}
	var token *tokenFile
	if cfg.TokenFile != "" {
		if cfg.Token != "" {
			return nil, errors.New("interceptor: Token or TokenFile, not both")
		}
		if cfg.TokenFileRefresh <= 0 {
			cfg.TokenFileRefresh = 30 * time.Second
		}
		token = &tokenFile{path: cfg.TokenFile, refresh: cfg.TokenFileRefresh}
		if err := token.load(); err != nil {
			return nil, err
		}
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
		cfg: cfg, conn: conn, api: pb.NewInterceptorClient(conn), record: record, token: token,
		verdicts: map[string]*callVerdicts{}, held: map[string][]job{}, idents: map[string]*callIdentity{},
		now: time.Now,
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

// Parser parses requests for OnRequest, with the deployment's inline limit.
func (c *Client) Parser() convo.Parser { return convo.Parser{InlineLimit: c.cfg.InlineLimit} }

// Unparsed counts a request (or client WebSocket message) convo couldn't read:
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

// Close waits for the calls held for their identity, drains the queues
// (bounded by ObserveTimeout per call) and closes the connection.
func (c *Client) Close() {
	c.awaiting.Wait() // calls waiting for their identity: at most their wait
	for _, q := range c.queues {
		close(q)
	}
	c.wg.Wait()
	c.conn.Close()
}

func (c *Client) ctx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, d)
	if t := c.bearer(); t != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+t)
	}
	return ctx, cancel
}

// bearer is the token every call carries: Token, or TokenFile's current one.
func (c *Client) bearer() string {
	if c.token != nil {
		return c.token.get()
	}
	return c.cfg.Token
}

// tokenFile is a bearer token kept in a file that something else rewrites
// (a projected service-account token: the kubelet replaces it, by an atomic
// symlink swap, well before it expires). Calls read the cached value; at most
// every refresh one of them re-reads the file, so no call waits on the disk
// for long and none sends a token older than refresh plus the writer's lead.
type tokenFile struct {
	path    string
	refresh time.Duration

	mu     sync.Mutex
	value  string
	readAt time.Time
}

// load reads the file, failing if it can't be read or holds nothing.
func (t *tokenFile) load() error {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return fmt.Errorf("interceptor: token file: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return fmt.Errorf("interceptor: token file %s is empty", t.path)
	}
	t.mu.Lock()
	t.value, t.readAt = v, time.Now()
	t.mu.Unlock()
	return nil
}

// get returns the current token, re-reading the file when it's due. A failed
// or empty read keeps the last token: a briefly missing file (mid-swap) must
// not turn every call unauthenticated.
func (t *tokenFile) get() string {
	t.mu.Lock()
	due := time.Since(t.readAt) >= t.refresh
	if due {
		t.readAt = time.Now() // one re-read per interval, even if it fails
	}
	v := t.value
	t.mu.Unlock()
	if due {
		if b, err := os.ReadFile(t.path); err == nil {
			if nv := strings.TrimSpace(string(b)); nv != "" {
				t.mu.Lock()
				t.value = nv
				t.mu.Unlock()
				return nv
			}
		}
	}
	return v
}

// ── Observe ──────────────────────────────────────────────────────────────────

// ObserveCall starts a call in observe mode: it queues OnCallStart and
// returns the Call the call's later callbacks carry. While ci's credential is
// being looked up, the call's callbacks (this one and every later one) are
// held in a buffer of the call's own until the lookup ends, or wait has
// passed since now; then its identity is resolved once and they're queued in
// order. Other calls never wait for it.
func (c *Client) ObserveCall(ci *CallInfo, wait time.Duration) *pb.Call {
	w := ci.who()
	r, id := w.resolve()
	call := ci.callWith(id)
	if ci.Lookup != nil && wait > 0 && !ci.Lookup.Wait(0) {
		c.hmu.Lock()
		c.held[ci.CallID] = nil
		c.holding.Add(1)
		// Its entry exists from now (identity to come): a stream that ends
		// while the call is held is marked ended, so the entry is bounded.
		c.setIdentityLocked(ci.CallID, nil)
		c.hmu.Unlock()
		c.heldCalls.Add(1)
		c.awaiting.Add(1)
		go c.await(ci.CallID, w, ci.Lookup, wait, time.Now())
	} else {
		w.ids.Note(r)
		c.setIdentity(ci.CallID, id)
	}
	c.ObserveStart(ci.startWith(ci.callWith(id)))
	return call
}

// await waits for a held call's lookup (up to wait from t0), resolves its
// identity and releases its callbacks.
func (c *Client) await(callID string, w who, p *identity.Pending, wait time.Duration, t0 time.Time) {
	defer c.awaiting.Done()
	p.Wait(wait - time.Since(t0))
	w.ids.NoteWait("observe", time.Since(t0))
	r, id := w.resolve()
	w.ids.Note(r)
	c.hmu.Lock()
	defer c.hmu.Unlock()
	c.setIdentityLocked(callID, id)
	jobs := c.held[callID]
	delete(c.held, callID)
	c.holding.Add(-1)
	for _, j := range jobs {
		c.push(j)
	}
}

// DecideCall starts a call in enforce mode: it waits up to wait for ci's
// credential lookup, resolves the call's identity once, and calls
// OnCallStart. It returns the Call the call's later callbacks carry, and the
// verdict.
func (c *Client) DecideCall(ctx context.Context, ci *CallInfo, wait time.Duration) (*pb.Call, Decision) {
	if ci.Lookup != nil {
		t := time.Now()
		if !ci.Lookup.Wait(0) {
			ci.Lookup.Wait(wait)
			ci.Identities.NoteWait("enforce", time.Since(t))
		}
	}
	w := ci.who()
	r, id := w.resolve()
	w.ids.Note(r)
	c.setIdentity(ci.CallID, id)
	return ci.callWith(id), c.DecideStart(ctx, ci.startWith(ci.callWith(id)))
}

// callIdentity is a started call's identity.
type callIdentity struct {
	id *pb.Identity // nil while the call is held for it
	// ended: when the call's stream ended (zero while it's open).
	ended time.Time
}

func (c *Client) setIdentity(callID string, id *pb.Identity) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	c.setIdentityLocked(callID, id)
}

// Ended is called when a call's stream has ended. Its identity is kept for
// its final record (which the processor delivers after this), and swept
// verdictTTL later if that record never comes. An open call's is never
// swept, however long it idles.
func (c *Client) Ended(callID string) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	if ci := c.idents[callID]; ci != nil {
		ci.ended = c.now()
	}
}

func (c *Client) setIdentityLocked(callID string, id *pb.Identity) {
	now := c.now()
	if len(c.idents)%256 == 0 {
		for k, ci := range c.idents {
			if !ci.ended.IsZero() && now.Sub(ci.ended) > verdictTTL {
				delete(c.idents, k)
			}
		}
	}
	if ci := c.idents[callID]; ci != nil {
		ci.id = id // a held call's, now known: its ended stays
		return
	}
	c.idents[callID] = &callIdentity{id: id}
}

// stamp gives a job being delivered its call's identity: every callback of a
// call carries the one resolved when it started. Only the job's own messages
// are changed.
func (c *Client) stamp(j *job) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	ci := c.idents[j.callID]
	if ci == nil || ci.id == nil {
		return
	}
	if j.final {
		delete(c.idents, j.callID)
	}
	switch {
	case j.start != nil:
		j.start.Call.Identity = ci.id
	case j.turn != nil:
		j.turn.call.Identity = ci.id
	case j.req != nil:
		j.req.call.Identity = ci.id
	case j.reply != nil:
		j.reply.msg.Call.Identity = ci.id
	case j.rec != nil:
		j.rec.Call.Identity = ci.id
	}
}

// ObserveStart queues OnCallStart (observe mode).
func (c *Client) ObserveStart(s *pb.CallStart) {
	c.enqueue(job{callID: s.GetCall().GetCallId(), start: s})
}

// Complete queues OnCallComplete (both modes). final marks the call's last
// record: the verdicts recorded for the call are attached and released.
func (c *Client) Complete(r *pb.CallRecord, final bool) {
	c.enqueue(job{callID: r.GetCall().GetCallId(), rec: r, final: final})
}

// enqueue queues a callback, or holds it with its call's (ObserveCall).
func (c *Client) enqueue(j job) {
	if c.holding.Load() > 0 {
		c.hmu.Lock()
		if h, ok := c.held[j.callID]; ok {
			if len(h) < c.cfg.QueueSize {
				c.held[j.callID] = append(h, j)
			} else if n := c.dropped.Add(1); n == 1 || n%1000 == 0 {
				slog.Warn("interceptor: a call held for its identity has too many callbacks: dropping (traffic unaffected)", "total_dropped", n)
			}
			c.hmu.Unlock()
			return
		}
		c.hmu.Unlock()
	}
	c.push(j)
}

// push puts a callback on its call's queue (never blocks).
func (c *Client) push(j job) {
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
	c.stamp(&j)
	ctx, cancel := c.ctx(context.Background(), c.cfg.ObserveTimeout)
	defer cancel()
	var err error
	switch {
	case j.start != nil:
		t := time.Now()
		var v *pb.CallVerdict
		if v, err = c.api.OnCallStart(ctx, j.start); err == nil {
			c.noteStart(j.callID, "call_start", "", v, false, time.Since(t))
		}
	case j.turn != nil:
		err = c.onTurnObserved(ctx, j.callID, j.turn)
	case j.req != nil:
		err = c.onRequestObserved(ctx, j.req)
	case j.reply != nil:
		err = c.onReplyObserved(ctx, j.callID, j.reply)
	case j.rec != nil:
		vs := c.verdictsFor(j.callID)
		j.rec.Verdicts, j.rec.Respond = recorded(vs), respondMode(vs)
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

// ── Recording ────────────────────────────────────────────────────────────────

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

// verdictsFor returns and releases the call's verdicts since its previous
// record: each record has the answers since the call's last one (a
// WebSocket turn's own).
func (c *Client) verdictsFor(callID string) []events.Verdict {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv := c.verdicts[callID]
	if cv == nil {
		return nil
	}
	delete(c.verdicts, callID)
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
	// Unparsed: requests that went upstream untouched, without
	// OnRequest, and replies that couldn't be read for OnReply and
	// OnToolCall (observe), and why.
	Unparsed        uint64            `json:"unparsed"`
	UnparsedReasons map[string]uint64 `json:"unparsed_reasons,omitempty"`
	// NoNewMessages: requests read whole that add no message (a
	// chained request resuming a turn), so had no OnRequest.
	NoNewMessages uint64 `json:"no_new_messages"`
	// HeldForIdentity: observed calls whose callbacks waited for their
	// credential's lookup (in a buffer of their own, not the queues).
	HeldForIdentity uint64 `json:"held_for_identity"`
}

// Stats returns the counters.
func (c *Client) Stats() Stats {
	return Stats{
		Endpoint: c.cfg.Endpoint, Content: c.cfg.Content,
		Queued: c.queued.Load(), Dropped: c.dropped.Load(), Delivered: c.delivered.Load(),
		Failed: c.failed.Load(), Decided: c.decided.Load(), DecideFailed: c.decideFailed.Load(),
		Unparsed: c.unparsed.Load(), UnparsedReasons: c.unparsedReasons(), NoNewMessages: c.noNew.Load(),
		HeldForIdentity: c.heldCalls.Load(),
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
