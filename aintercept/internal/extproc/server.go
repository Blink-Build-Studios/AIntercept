// Package extproc implements the Envoy ext_proc ExternalProcessor server for
// the observer.
//
// The golden rule here is: ACK ENVOY IMMEDIATELY. Every ProcessingRequest is
// answered with a minimal "continue, no modifications" ProcessingResponse as
// fast as possible. We copy the observation into an Event and hand it to the
// Processor (a bounded, non-blocking buffer) — we never do real work, I/O, or
// sink writes on the request-handling path. This is what keeps a slow or failed
// sink from ever adding latency to real API traffic.
package extproc

import (
	"crypto/hmac"
	"encoding/base64"
	"io"
	"strings"
	"sync/atomic"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	filterextprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/probe"
)

// Server is the ext_proc gRPC handler.
type Server struct {
	extprocv3.UnimplementedExternalProcessorServer

	proc           *events.Processor
	budget         *membudget.Budget
	instanceID     string
	devLogBodies   bool
	bodyPreviewCap int
	// captureFullBodies stores complete, un-truncated body chunks in BodyFull.
	// Dev-only; must be paired with a durable file sink (not stdout).
	captureFullBodies bool
	// keyHashSalt is the per-deployment secret mixed into API-key hashes so the
	// pseudonymous identity is stable in-deployment but not cross-correlatable.
	keyHashSalt string
	// keyHintPrefix/Suffix control the display-only key fragment (first N / last M
	// chars) stamped as KeyHint alongside the hash.
	keyHintPrefix int
	keyHintSuffix int
	// clientIPHeaders are request headers trusted to carry the original client
	// IP, checked in order before the Envoy-observed peer address. Empty (the
	// default) means only the peer address is used. See events.ClientIP.
	clientIPHeaders []string
	// probeMarker identifies the observer's own health probes; requests
	// carrying it are tagged Synthetic. Empty disables tagging.
	probeMarker string
	// ic is the customer's Interceptor (nil: none). See intercept.go and
	// enforce.go.
	ic *interceptor.Client
	// full reports the writer's spool full (nil: never). While it does,
	// enforced streams are refused so no enforced call goes unrecorded.
	full func() bool

	streamCounter    atomic.Uint64
	undecodableCount atomic.Uint64
}

// Config configures the ext_proc Server.
type Config struct {
	Processor *events.Processor
	// Budget measures the bytes streams hold (nil: not measured). Pass the
	// same Budget to the Processor, which releases what events carry.
	Budget            *membudget.Budget
	InstanceID        string
	DevLogBodies      bool
	BodyPreviewCap    int
	CaptureFullBodies bool
	KeyHashSalt       string
	KeyHintPrefix     int
	KeyHintSuffix     int
	ClientIPHeaders   []string
	ProbeMarker       string
	// Interceptor, when set, receives every call (docs/INTERCEPTOR.md).
	Interceptor *interceptor.Client
	// Full reports the writer's spool full. While it does, enforced streams
	// are refused (Envoy's enforcing filter, failure_mode_allow: false, then
	// rejects the call); observed streams carry on. nil: never full.
	Full func() bool
}

// New builds a Server.
func New(cfg Config) *Server {
	cap := cfg.BodyPreviewCap
	if cap <= 0 {
		cap = 256
	}
	prefix := cfg.KeyHintPrefix
	if prefix <= 0 {
		prefix = 6
	}
	suffix := cfg.KeyHintSuffix
	if suffix <= 0 {
		suffix = 4
	}
	return &Server{
		proc:              cfg.Processor,
		budget:            cfg.Budget,
		instanceID:        cfg.InstanceID,
		devLogBodies:      cfg.DevLogBodies,
		bodyPreviewCap:    cap,
		captureFullBodies: cfg.CaptureFullBodies,
		keyHashSalt:       cfg.KeyHashSalt,
		keyHintPrefix:     prefix,
		keyHintSuffix:     suffix,
		clientIPHeaders:   cfg.ClientIPHeaders,
		probeMarker:       cfg.ProbeMarker,
		ic:                cfg.Interceptor,
		full:              cfg.Full,
	}
}

// streamState holds per-ext_proc-stream correlation context. One ext_proc
// stream corresponds to one HTTP transaction, so this lives entirely on the
// goroutine serving Process — no shared state, no sticky-routing requirement.
type streamState struct {
	correlationID string
	seq           uint64
	host          string
	method        string
	path          string
	status        int
	reqBytes      int
	respBytes     int
	// When the request and response headers arrived (Unix ns): the header rows'
	// timestamps, repeated on the complete row so a call needs no join.
	reqStartNS  int64
	respStartNS int64

	// Usage-extraction state. We buffer just enough of the request/response
	// bodies to parse the model + token counts, then DISCARD the content — no
	// prompt/response text is ever stored. Buffers are capped to bound memory.
	// The parse itself happens off this goroutine (see emitComplete / the
	// Processor drain), so the buffers are only accumulated here.
	respEncoding    string
	reqBuf          []byte
	respBuf         []byte
	reqTrunc        bool
	respTrunc       bool
	respTail        *sseTail // a streamed response's final events, once respBuf is full
	completeEmitted bool

	// Pseudonymous caller identity, derived once from the request auth header and
	// carried onto the complete event so usage rows and unrecognized-shape samples
	// can be attributed. The raw key is never stored.
	keyHash string
	keyHint string
	auth    *events.AuthIdentity
	// held is the bytes this stream holds in buffers, charged to the memory
	// budget (see account); handed to events with the buffers they carry.
	held      int64
	ip        string
	synthetic bool
	// WebSocket: wsRequested is set by an upgrade request; ws once the server
	// answers 101, after which body bytes go to the frame decoders.
	wsRequested bool
	ws          *wsState
	// Redacted request/response headers, retained for unrecognized-shape
	// fingerprinting on the complete event. Values of sensitive headers are
	// already masked by RedactHeaders; the fingerprinter further value-drops all
	// but a non-secret marker allowlist.
	reqHeaders  map[string]string
	respHeaders map[string]string

	// enforce: the stream is from the enforcing filter (full duplex), where
	// this server forwards every byte (enforce.go). enf is its state; ic the
	// Interceptor bookkeeping in either mode.
	enforce bool
	enf     *enforceState
	ic      *interceptState
	// denyReason is set when enforce refused the call; it becomes the
	// complete row's Error.
	denyReason string
}

// Per-stream body buffering caps for metadata extraction.
//
// The REQUEST body must be buffered whole to parse as JSON (model + the tool
// catalog / tool_use history), so its cap is generous — a Claude Code request
// with a long conversation and many MCP tools can be several MB. A request that
// overflows the cap yields no tool/model data (truncated JSON) but never affects
// the proxied stream.
//
// The RESPONSE buffer keeps the head of the body (the model, tool calls, input
// usage). A streamed response past the cap still keeps its final events, which
// carry the output usage and stop reason (sseTail): Codex responses run to 1 MB.
const (
	reqBufferCap  = 4 * 1024 * 1024
	respBufferCap = 512 * 1024
)

func (s *streamState) next() uint64 {
	s.seq++
	return s.seq
}

// Process handles one bidirectional ext_proc stream.
func (srv *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	id := srv.streamCounter.Add(1)
	st := &streamState{correlationID: newCorrelationID(srv.instanceID, id)}
	// Whatever the stream still holds when it ends is released.
	defer func() { srv.budget.Add(-st.held) }()

	for first := true; ; first = false {
		req, err := stream.Recv()
		if first && err == nil && req.GetProtocolConfig().GetRequestBodyMode() == extprocv3ProcessingModeFullDuplex {
			if srv.full != nil && srv.full() {
				return status.Error(codes.Unavailable, "aintercept: the observer's spool is full; enforced calls are refused until it drains")
			}
			return srv.processEnforce(stream, st, req)
		}
		if err == io.EOF {
			srv.wsClose(st)
			return nil
		}
		if err != nil {
			srv.wsClose(st)
			// Downstream/filter went away. Record a disconnect observation.
			srv.offer(st, events.Event{
				Phase: events.PhaseDisconnect,
				Error: err.Error(),
			})
			return err
		}

		resp := srv.handle(st, req)

		// In observability mode Envoy doesn't wait for (and ignores) replies.
		if req.GetObservabilityMode() {
			continue
		}
		// ACK immediately. If the send fails the stream is dead; return.
		if err := stream.Send(resp); err != nil {
			srv.wsClose(st)
			return err
		}
	}
}

// handle extracts an observation from req, offers it to the processor
// (non-blocking), and returns the minimal continue response for this phase.
func (srv *Server) handle(st *streamState, req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch v := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		st.ip = peerAddress(req)
		srv.observeHeaders(st, events.DirInbound, events.PhaseRequestHeaders, v.RequestHeaders)
		return continueRequestHeaders()

	case *extprocv3.ProcessingRequest_ResponseHeaders:
		srv.observeHeaders(st, events.DirOutbound, events.PhaseResponseHeaders, v.ResponseHeaders)
		return continueResponseHeaders()

	case *extprocv3.ProcessingRequest_RequestBody:
		srv.observeBody(st, events.DirInbound, events.PhaseRequestBodyChunk, v.RequestBody)
		return continueRequestBody()

	case *extprocv3.ProcessingRequest_ResponseBody:
		srv.observeBody(st, events.DirOutbound, events.PhaseResponseBodyChunk, v.ResponseBody)
		return continueResponseBody()

	case *extprocv3.ProcessingRequest_RequestTrailers:
		return continueRequestTrailers()

	case *extprocv3.ProcessingRequest_ResponseTrailers:
		// Trailer-terminated responses (final data frame had end_of_stream=false)
		// complete HERE. emitComplete is idempotent, so if the body already ended
		// the stream this is a no-op — exactly one complete event per transaction,
		// and usage is extracted either way.
		srv.emitComplete(st)
		return continueResponseTrailers()

	default:
		// Unknown phase: continue without observing.
		return continueRequestHeaders()
	}
}

func (srv *Server) observeHeaders(st *streamState, dir events.Direction, phase events.Phase, h *extprocv3.HttpHeaders) {
	hdrs := flattenHeaders(h.GetHeaders())
	// Pull pseudo-headers for correlation metadata. On the inbound (request) side
	// also derive the pseudonymous caller identity (salted key hash) and source
	// IP from the auth/XFF headers BEFORE they are redacted — the raw key is never
	// stored, only its salted hash.
	var keyHash, keyHint, ip string
	if dir == events.DirInbound {
		if v := hdrs[":authority"]; v != "" {
			st.host = v
		}
		if v := hdrs[":method"]; v != "" {
			st.method = v
		}
		if v := hdrs[":path"]; v != "" {
			// Credential-bearing query params (Gemini `?key=`) never reach an
			// event; the key hash below is computed from the raw headers first.
			st.path = events.RedactPath(v)
		}
		keyHash = events.HashAPIKey(hdrs, srv.keyHashSalt)
		keyHint = events.KeyHint(hdrs, srv.keyHintPrefix, srv.keyHintSuffix)
		ip = events.ClientIP(hdrs, srv.clientIPHeaders, st.ip)
		st.ip = ip
		st.synthetic = srv.probeMarker != "" &&
			hmac.Equal([]byte(hdrs[probe.MarkerHeader]), []byte(srv.probeMarker))
		st.wsRequested = strings.EqualFold(hdrs["upgrade"], "websocket")
		st.keyHash = keyHash
		st.keyHint = keyHint
		st.auth = events.AuthIdentityFromHeaders(hdrs)
	} else {
		if v := hdrs[":status"]; v != "" {
			st.status = atoiSafe(v)
		}
		// Remember the response encoding so usage extraction can decompress.
		for k, v := range hdrs {
			if strings.EqualFold(k, "content-encoding") {
				st.respEncoding = v
			}
		}
	}

	// Redact ONCE and reuse the copy for both the emitted header event and the
	// stream-state retention used by the complete event / shape fingerprinter.
	redacted := events.RedactHeaders(hdrs)
	if dir == events.DirInbound {
		st.reqHeaders = redacted
	} else {
		st.respHeaders = redacted
		if st.wsRequested && st.status == 101 && st.ws == nil {
			st.ws = newWSState(hdrs)
		}
	}
	defer func() {
		// The request has arrived with no body (or is a WebSocket upgrade):
		// the Interceptor's OnCallStart, after the header row.
		if dir == events.DirInbound && (h.GetEndOfStream() || st.wsRequested) {
			srv.observeStart(st)
		}
	}()

	now := time.Now().UnixNano()
	if dir == events.DirInbound {
		st.reqStartNS = now
	} else {
		st.respStartNS = now
	}
	srv.offer(st, events.Event{
		TimestampNS: now,
		Phase:       phase,
		Direction:   dir,
		Host:        st.host,
		Method:      st.method,
		Path:        st.path,
		Status:      st.status,
		KeyHash:     keyHash,
		KeyHint:     keyHint,
		IP:          ip,
		Synthetic:   st.synthetic,
		Headers:     redacted,
		EndOfStream: h.GetEndOfStream(),
	})
}

func (srv *Server) observeBody(st *streamState, dir events.Direction, phase events.Phase, b *extprocv3.HttpBody) {
	n := len(b.GetBody())
	switch {
	case st.ws != nil && st.enf != nil && st.enf.wsContent:
		// Enforced with content: enforceWS decodes the frames (once) and
		// records the messages; only the byte counts are kept here.
		if dir == events.DirInbound {
			st.ws.conn.BytesUp += n
		} else {
			st.ws.conn.BytesDown += n
		}
	case st.ws != nil:
		// Upgraded WebSocket: decode frames instead of buffering a body.
		srv.wsFeed(st, dir, b.GetBody())
	case dir == events.DirInbound:
		st.reqBytes += n
		st.reqBuf, st.reqTrunc = appendCapped(st.reqBuf, b.GetBody(), st.reqTrunc, reqBufferCap)
	default:
		st.respBytes += n
		body := b.GetBody()
		if st.respEncoding == "" && (st.respTrunc || len(st.respBuf)+len(body) > respBufferCap) {
			over := body
			if !st.respTrunc {
				over = body[max(0, respBufferCap-len(st.respBuf)):]
			}
			if st.respTail == nil {
				st.respTail = &sseTail{}
			}
			st.respTail.feed(over)
		}
		st.respBuf, st.respTrunc = appendCapped(st.respBuf, body, st.respTrunc, respBufferCap)
	}

	e := events.Event{
		Phase:       phase,
		Direction:   dir,
		Host:        st.host,
		Method:      st.method,
		Path:        st.path,
		Status:      st.status,
		Bytes:       n,
		EndOfStream: b.GetEndOfStream(),
	}
	// Body preview is a debugging aid (DEV_LOG_BODIES), off by default and
	// strictly capped. Restricted to the RESPONSE (outbound) side: previewing the
	// request body would echo raw prompt text to stdout.
	if srv.devLogBodies && n > 0 && dir == events.DirOutbound {
		e.BodyPreview = previewBytes(b.GetBody(), srv.bodyPreviewCap)
	}
	// Full interception (INTERCEPTION_MODE=full): store the complete chunk as
	// base64 of the RAW bytes so compressed/binary response bodies survive
	// losslessly. Only the full-interception sinks receive it; stdout and the
	// content-free stream strip it (Event.ForStdout).
	if srv.captureFullBodies && n > 0 {
		e.BodyB64 = base64.StdEncoding.EncodeToString(b.GetBody())
	}
	srv.offer(st, e)

	if st.ws == nil {
		if dir == events.DirInbound && b.GetEndOfStream() {
			srv.observeStart(st)
		} else if dir == events.DirOutbound {
			srv.observeResponseContent(st, b.GetBody(), b.GetEndOfStream())
		}
	}
	if b.GetEndOfStream() && dir == events.DirOutbound {
		if st.ws != nil {
			srv.wsClose(st)
		} else {
			srv.emitComplete(st)
		}
	}
	srv.account(st)
}

// account charges the memory budget for what the stream's buffers hold now:
// the HTTP body buffers, or a WebSocket's decoder state and turn in progress.
func (srv *Server) account(st *streamState) {
	n := int64(len(st.reqBuf) + len(st.respBuf))
	if st.respTail != nil {
		n += int64(st.respTail.held())
	}
	if ws := st.ws; ws != nil {
		n += int64(ws.up.Buffered() + ws.down.Buffered())
		if t := ws.turn; t != nil {
			n += int64(len(t.req) + len(t.resp))
		}
	}
	srv.budget.Add(n - st.held)
	st.held = n
}

// handOff moves n of the stream's budgeted bytes to e, which carries those
// buffers to the processor (which releases them after extraction).
func (srv *Server) handOff(st *streamState, e *events.Event, n int) {
	e.Hold(int64(n))
	st.held -= int64(n)
}

// emitComplete emits the single authoritative `complete` event for a
// transaction, with the buffered bodies ATTACHED for deferred (off-hot-path)
// extraction of model/tokens/tools. It is idempotent: the first caller (response
// body end-of-stream, or the trailers path when the body didn't end the stream)
// wins; subsequent calls are no-ops, so a response that ends with both a final
// data frame and trailers produces exactly one complete event (fixes the
// double-complete / usage-less-complete bug). The heavy JSON parse happens in the
// Processor drain goroutine via Event.ExtractDeferred, never here.
func (srv *Server) emitComplete(st *streamState) {
	if st.completeEmitted || st.ws != nil { // a WebSocket emits turns and a connection row instead
		return
	}
	st.completeEmitted = true
	complete := events.Event{
		Phase:           events.PhaseComplete,
		Host:            st.host,
		Method:          st.method,
		Path:            st.path,
		Status:          st.status,
		KeyHash:         st.keyHash,
		KeyHint:         st.keyHint,
		Auth:            st.auth,
		IP:              st.ip,
		ProviderOrgID:   events.ProviderOrgID(st.respHeaders),
		Synthetic:       st.synthetic,
		EndOfStream:     true,
		RequestStartNS:  st.reqStartNS,
		ResponseStartNS: st.respStartNS,
		RequestBytes:    st.reqBytes,
		ResponseBytes:   st.respBytes,
		InterceptMode:   st.interceptMode(),
		Error:           st.denyReason,
	}
	// Attach the buffers for deferred parsing. A truncated request buffer yields
	// invalid JSON downstream (no model/tools) rather than an error; pass it
	// anyway since the model often sits near the front. Content is never stored.
	var req []byte
	if !st.reqTrunc || len(st.reqBuf) > 0 {
		req = st.reqBuf
	}
	resp := withTail(st.respBuf, st.respTail)
	complete.AttachBodies(req, resp, st.respEncoding, st.respHeaders)
	held := len(st.reqBuf) + len(resp)
	if st.respTail != nil {
		held = len(st.reqBuf) + len(st.respBuf) + st.respTail.held()
	}
	srv.handOff(st, &complete, held)
	// Request headers ride the same unexported carrier (never serialized onto the
	// normal complete event); only the unrecognized-shape fingerprinter reads them.
	complete.AttachRequestHeaders(st.reqHeaders)
	srv.offer(st, complete)
	// Release the per-stream buffers; the attached copies live on the event until
	// the drain goroutine extracts and clears them.
	st.reqBuf, st.respBuf, st.respTail = nil, nil, nil
}

// appendCapped appends src to dst until dst reaches cap, then stops (marking
// truncated). The cap bounds memory regardless of body size; a truncated buffer
// simply yields no parsed metadata rather than erroring the request path.
func appendCapped(dst, src []byte, truncated bool, limit int) ([]byte, bool) {
	if truncated || len(dst) >= limit {
		return dst, true
	}
	room := limit - len(dst)
	add := src
	trunc := false
	if len(src) > room {
		add, trunc = src[:room], true
	}
	// Grow precisely rather than letting append's doubling overshoot the limit
	// (a 4 MiB limit otherwise backs a ~4.6 MiB array). Bounds per-stream memory
	// to ~limit, which matters when many large streams are in flight.
	if cap(dst)-len(dst) < len(add) {
		want := len(dst) + len(add)
		grown := make([]byte, len(dst), want)
		copy(grown, dst)
		dst = grown
	}
	return append(dst, add...), trunc
}

// offer stamps the shared fields and hands the event to the processor.
// Enforced streams never drop (OfferMust); observed ones drop only as the
// counted last resort while the spool is full.
func (srv *Server) offer(st *streamState, e events.Event) {
	e.CorrelationID = st.correlationID
	e.Seq = st.next()
	if e.TimestampNS == 0 {
		e.TimestampNS = time.Now().UnixNano()
	}
	e.ObserverInstance = srv.instanceID
	if st.enforce {
		srv.proc.OfferMust(e)
		return
	}
	srv.proc.Offer(e) // releases e's held bytes itself if it drops e
}

// extProcAttributesKey is the key Envoy files request_attributes under in
// ProcessingRequest.attributes: the ext_proc filter's canonical name.
const extProcAttributesKey = "envoy.filters.http.ext_proc"

// peerAddress returns the downstream peer ("ip:port") Envoy observed on the
// connection, sent via the filter's request_attributes: ["source.address"].
// With PROXY protocol on the listener this is the original client, not the
// load balancer. Empty when Envoy wasn't configured to send it.
func peerAddress(req *extprocv3.ProcessingRequest) string {
	attrs := req.GetAttributes()[extProcAttributesKey]
	return attrs.GetFields()["source.address"].GetStringValue()
}

func flattenHeaders(hm *corev3.HeaderMap) map[string]string {
	if hm == nil {
		return nil
	}
	out := make(map[string]string, len(hm.GetHeaders()))
	for _, h := range hm.GetHeaders() {
		v := h.GetValue()
		if v == "" && len(h.GetRawValue()) > 0 {
			v = string(h.GetRawValue())
		}
		out[strings.ToLower(h.GetKey())] = v
	}
	return out
}

func previewBytes(b []byte, cap int) string {
	if len(b) > cap {
		b = b[:cap]
	}
	// Keep it printable-ish; this is a dev aid, not a parser.
	return strings.ToValidUTF8(string(b), "?")
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// extprocv3ProcessingModeFullDuplex is the body mode of the enforcing filter.
const extprocv3ProcessingModeFullDuplex = filterextprocv3.ProcessingMode_FULL_DUPLEX_STREAMED

// interceptMode is the stream's mode as recorded on its rows.
func (st *streamState) interceptMode() string {
	if st.enforce {
		return "enforce"
	}
	return ""
}
