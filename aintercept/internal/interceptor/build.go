package interceptor

import (
	"strings"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
)

// CallInfo is what the observer knows about a call when it starts.
type CallInfo struct {
	CallID    string
	Enforce   bool
	WebSocket bool
	Host      string
	Method    string
	Path      string
	StartNS   int64
	KeyHash   string
	KeyHint   string
	IP        string
	Auth      *events.AuthIdentity
	// Headers are the request headers, credentials redacted.
	Headers      map[string]string
	Summary      events.RequestSummary
	RequestBytes int
	// Identities is the pod's credential owners (nil: the call alone says
	// who it is); Lookup the credential's lookup in flight (nil: none).
	Identities *identity.Cache
	Lookup     *identity.Pending
}

func mode(enforce bool) pb.Mode {
	if enforce {
		return pb.Mode_MODE_ENFORCE
	}
	return pb.Mode_MODE_OBSERVE
}

func transport(ws bool) pb.Transport {
	if ws {
		return pb.Transport_TRANSPORT_WEBSOCKET
	}
	return pb.Transport_TRANSPORT_HTTP
}

// Call builds the Call every callback carries, its identity resolved from
// what's known now (never waits).
func (ci *CallInfo) Call() *pb.Call { return ci.callWith(ci.who().identity()) }

func (ci *CallInfo) callWith(id *pb.Identity) *pb.Call {
	return &pb.Call{
		CallId: ci.CallID, Mode: mode(ci.Enforce), Transport: transport(ci.WebSocket),
		Host: ci.Host, Method: ci.Method, Path: ci.Path, StartUnixNano: ci.StartNS,
		Identity: id,
		Client: &pb.Client{
			Product: ci.Summary.Product, UserAgent: ci.Headers["user-agent"], Headers: ci.Summary.Client,
		},
	}
}

// who is what a call says about who makes it, copied: it can be resolved
// later, on another goroutine (the hold, client.go).
type who struct {
	ids                                   *identity.Cache
	q                                     identity.Call
	keyHash, keyHint, ip, device, session string
}

func (ci *CallInfo) who() who {
	w := who{ids: ci.Identities, keyHash: ci.KeyHash, keyHint: ci.KeyHint, ip: ci.IP,
		device: ci.Summary.DeviceID, session: ci.Summary.SessionID}
	org := ci.Headers["x-organization-uuid"]
	if org == "" {
		org = ci.Headers["openai-organization"]
	}
	w.q = identity.Call{KeyHash: ci.KeyHash, Reported: ci.Summary.AccountID, OrgID: org}
	if a := ci.Auth; a != nil {
		w.q.Reported = firstNonEmpty(w.q.Reported, a.AccountID)
		w.q.Claims = &events.AuthIdentity{Email: a.Email, Name: a.Name}
		w.session = firstNonEmpty(w.session, a.SessionID)
	}
	return w
}

// resolve is the identity from what's known now.
func (w who) resolve() (identity.Identity, *pb.Identity) {
	r := w.ids.Resolve(w.q)
	id := identityOf(r)
	id.KeyHash, id.KeyHint, id.Ip, id.DeviceId, id.SessionId = w.keyHash, w.keyHint, w.ip, w.device, w.session
	return r, id
}

func (w who) identity() *pb.Identity {
	_, id := w.resolve()
	return id
}

// identityOf is a resolved identity on the wire.
func identityOf(r identity.Identity) *pb.Identity {
	return &pb.Identity{
		OrgId: r.OrgID, Email: r.Email, Name: r.Name, EmailSource: pb.IdentitySource(r.Source),
		ReportedAccountId: r.Reported, VerifiedAccountId: r.Verified, AccountMismatch: r.Mismatch,
	}
}

// Start builds OnCallStart's message.
func (ci *CallInfo) Start() *pb.CallStart { return ci.startWith(ci.Call()) }

func (ci *CallInfo) startWith(call *pb.Call) *pb.CallStart {
	return &pb.CallStart{Call: call, Headers: ci.Headers, Request: requestInfo(ci.Summary, ci.RequestBytes)}
}

func requestInfo(s events.RequestSummary, bytes int) *pb.RequestInfo {
	ri := &pb.RequestInfo{
		Model: s.Model, Effort: s.Effort, Stream: s.Stream, MaxTokens: int32(s.MaxTokens),
		Prewarm: s.Prewarm, Attachments: int32(s.Attachments), RequestBytes: int64(bytes),
	}
	for _, t := range s.ToolsAvailable {
		ri.ToolsAvailable = append(ri.ToolsAvailable, t.Name)
		if t.Kind == "mcp" {
			ri.McpToolsAvailable = append(ri.McpToolsAvailable, t.Server+"/"+t.Tool)
		}
	}
	return ri
}

// Message builds an OnMessage message.
func Message(call *pb.Call, dir events.Direction, seq int64, kind pb.MessageKind, payload []byte, contentType, sseEvent string) *pb.Message {
	d := pb.Direction_DIRECTION_SERVER_TO_CLIENT
	if dir == events.DirInbound {
		d = pb.Direction_DIRECTION_CLIENT_TO_SERVER
	}
	return &pb.Message{Call: call, Direction: d, Seq: seq, Kind: kind, Payload: payload, ContentType: contentType, SseEvent: sseEvent}
}

// Record builds OnCallComplete's message from a complete event (or a
// WebSocket connection row) after metadata extraction. It reports whether the
// record is the call's last (an HTTP call's complete row, a WebSocket's
// connection row). Its identity is resolved from ids now; the Client gives
// it the identity the call's other callbacks carried, when it has it.
func Record(e *events.Event, ids *identity.Cache) (*pb.CallRecord, bool) {
	ws := e.Transport == events.TransportWebSocket
	q := identity.Call{KeyHash: e.KeyHash, Reported: e.AccountUUID, OrgID: e.ProviderOrgID}
	if a := e.Auth; a != nil {
		q.Reported = firstNonEmpty(q.Reported, a.AccountID)
		q.Claims = a
	}
	id := identityOf(ids.Resolve(q))
	id.KeyHash, id.KeyHint, id.Ip, id.DeviceId, id.SessionId = e.KeyHash, e.KeyHint, e.IP, e.DeviceID, e.SessionID
	call := &pb.Call{
		CallId: e.CorrelationID, Mode: mode(e.InterceptMode == "enforce"), Transport: transport(ws),
		Host: e.Host, Method: e.Method, Path: e.Path, StartUnixNano: e.RequestStartNS,
		Identity: id,
		Client:   &pb.Client{Headers: e.Client},
	}
	if e.Client != nil {
		call.Client.UserAgent = e.Client["user-agent"]
		call.Client.Product = events.ProductOf(e.Host, e.Client["user-agent"], e.Client["originator"], e.SessionID)
	}
	req := &pb.RequestInfo{
		Model: e.Model, Effort: firstNonEmpty(e.Effort, e.ThinkingType), Stream: e.Stream, MaxTokens: int32(e.MaxTokens),
		Prewarm: e.Prewarm, RequestBytes: int64(e.RequestBytes),
	}
	for _, t := range e.ToolsAvailable {
		req.ToolsAvailable = append(req.ToolsAvailable, t.Name)
		if t.Kind == "mcp" {
			req.McpToolsAvailable = append(req.McpToolsAvailable, t.Server+"/"+t.Tool)
		}
	}
	if a := e.Attachments; a != nil {
		req.Attachments = int32(a.Images + a.Files)
	}
	resp := &pb.ResponseInfo{
		Status: int32(e.Status), Model: e.Model,
		InputTokens: int64(e.InputTokens), OutputTokens: int64(e.OutputTokens),
		CacheReadTokens: int64(e.CacheReadTokens), CacheWriteTokens: int64(e.CacheWriteTokens),
		ReasoningTokens: int64(e.ReasoningTokens), StopReason: e.StopReason,
		ResponseBytes: int64(e.ResponseBytes), Error: e.Error,
	}
	for _, t := range e.ToolUses {
		resp.ToolCalls = append(resp.ToolCalls, &pb.ToolCount{Name: t.Name, Count: int32(t.Count)})
	}
	if e.RequestStartNS > 0 {
		if e.ResponseStartNS > 0 {
			resp.TtfbMs = (e.ResponseStartNS - e.RequestStartNS) / 1e6
		}
		resp.DurationMs = (e.TimestampNS - e.RequestStartNS) / 1e6
	}
	final := !ws
	if e.Phase == events.PhaseWSConnection {
		final = true
		req, resp = nil, &pb.ResponseInfo{Status: int32(e.Status)}
		if c := e.WebSocket; c != nil {
			resp.ResponseBytes = int64(c.BytesDown)
			resp.DurationMs = c.DurationMS
		}
	}
	return &pb.CallRecord{
		Call: call, Request: req, Response: resp, EndUnixNano: e.TimestampNS, WsTurn: int32(e.WSTurn),
	}, final
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
