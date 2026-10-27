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
	"encoding/base64"
	"io"
	"strings"
	"sync/atomic"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/observer/internal/events"
)

// Server is the ext_proc gRPC handler.
type Server struct {
	extprocv3.UnimplementedExternalProcessorServer

	proc           *events.Processor
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

	streamCounter atomic.Uint64
}

// Config configures the ext_proc Server.
type Config struct {
	Processor         *events.Processor
	InstanceID        string
	DevLogBodies      bool
	BodyPreviewCap    int
	CaptureFullBodies bool
	KeyHashSalt       string
	KeyHintPrefix     int
	KeyHintSuffix     int
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
		instanceID:        cfg.InstanceID,
		devLogBodies:      cfg.DevLogBodies,
		bodyPreviewCap:    cap,
		captureFullBodies: cfg.CaptureFullBodies,
		keyHashSalt:       cfg.KeyHashSalt,
		keyHintPrefix:     prefix,
		keyHintSuffix:     suffix,
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
	completeEmitted bool

	// Pseudonymous caller identity, derived once from the request auth header and
	// carried onto the complete event so usage rows and unrecognized-shape samples
	// can be attributed. The raw key is never stored.
	keyHash string
	keyHint string
	// Redacted request/response headers, retained for unrecognized-shape
	// fingerprinting on the complete event. Values of sensitive headers are
	// already masked by RedactHeaders; the fingerprinter further value-drops all
	// but a non-secret marker allowlist.
	reqHeaders  map[string]string
	respHeaders map[string]string
}

// Per-stream body buffering caps for metadata extraction.
//
// The REQUEST body must be buffered whole to parse as JSON (model + the tool
// catalog / tool_use history), so its cap is generous — a Claude Code request
// with a long conversation and many MCP tools can be several MB. A request that
// overflows the cap yields no tool/model data (truncated JSON) but never affects
// the proxied stream.
//
// The RESPONSE body only needs the small SSE control frames that carry token
// usage, so a few hundred KB is ample.
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

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// Downstream/filter went away. Record a disconnect observation.
			srv.offer(st, events.Event{
				Phase: events.PhaseDisconnect,
				Error: err.Error(),
			})
			return err
		}

		resp := srv.handle(st, req)

		// ACK immediately. If the send fails the stream is dead; return.
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// handle extracts an observation from req, offers it to the processor
// (non-blocking), and returns the minimal continue response for this phase.
func (srv *Server) handle(st *streamState, req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch v := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
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
			st.path = v
		}
		keyHash = events.HashAPIKey(hdrs, srv.keyHashSalt)
		keyHint = events.KeyHint(hdrs, srv.keyHintPrefix, srv.keyHintSuffix)
		ip = events.ClientIP(hdrs)
		st.keyHash = keyHash
		st.keyHint = keyHint
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
	}

	srv.offer(st, events.Event{
		Phase:       phase,
		Direction:   dir,
		Host:        st.host,
		Method:      st.method,
		Path:        st.path,
		Status:      st.status,
		KeyHash:     keyHash,
		KeyHint:     keyHint,
		IP:          ip,
		Headers:     redacted,
		EndOfStream: h.GetEndOfStream(),
	})
}

func (srv *Server) observeBody(st *streamState, dir events.Direction, phase events.Phase, b *extprocv3.HttpBody) {
	n := len(b.GetBody())
	if dir == events.DirInbound {
		st.reqBytes += n
		st.reqBuf, st.reqTrunc = appendCapped(st.reqBuf, b.GetBody(), st.reqTrunc, reqBufferCap)
	} else {
		st.respBytes += n
		st.respBuf, st.respTrunc = appendCapped(st.respBuf, b.GetBody(), st.respTrunc, respBufferCap)
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
	// Body preview is OFF unless explicitly enabled for local dev, and even then
	// strictly capped. This is how we prove decrypted bytes reached Go without
	// routinely logging payloads. Restricted to the RESPONSE (outbound) side:
	// previewing the request body would echo raw prompt text to stdout.
	if srv.devLogBodies && n > 0 && dir == events.DirOutbound {
		e.BodyPreview = previewBytes(b.GetBody(), srv.bodyPreviewCap)
	}
	// Full-body capture (dev-only, file sink only): store the complete chunk as
	// base64 of the RAW bytes so compressed/binary response bodies survive
	// losslessly. Stripped from stdout by Event.ForStdout.
	if srv.captureFullBodies && n > 0 {
		e.BodyB64 = base64.StdEncoding.EncodeToString(b.GetBody())
	}
	srv.offer(st, e)

	if b.GetEndOfStream() && dir == events.DirOutbound {
		srv.emitComplete(st)
	}
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
	if st.completeEmitted {
		return
	}
	st.completeEmitted = true
	complete := events.Event{
		Phase:       events.PhaseComplete,
		Host:        st.host,
		Method:      st.method,
		Path:        st.path,
		Status:      st.status,
		KeyHash:     st.keyHash,
		KeyHint:     st.keyHint,
		EndOfStream: true,
	}
	// Attach the buffers for deferred parsing. A truncated request buffer yields
	// invalid JSON downstream (no model/tools) rather than an error; pass it
	// anyway since the model often sits near the front. Content is never stored.
	var req []byte
	if !st.reqTrunc || len(st.reqBuf) > 0 {
		req = st.reqBuf
	}
	complete.AttachBodies(req, st.respBuf, st.respEncoding, st.respHeaders)
	// Request headers ride the same unexported carrier (never serialized onto the
	// normal complete event); only the unrecognized-shape fingerprinter reads them.
	complete.AttachRequestHeaders(st.reqHeaders)
	srv.offer(st, complete)
	// Release the per-stream buffers; the attached copies live on the event until
	// the drain goroutine extracts and clears them.
	st.reqBuf, st.respBuf = nil, nil
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

// offer stamps the shared fields and hands the event to the processor. Never
// blocks.
func (srv *Server) offer(st *streamState, e events.Event) {
	e.CorrelationID = st.correlationID
	e.Seq = st.next()
	e.TimestampNS = time.Now().UnixNano()
	e.ObserverInstance = srv.instanceID
	srv.proc.Offer(e)
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
