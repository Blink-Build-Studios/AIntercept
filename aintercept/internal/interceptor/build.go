package interceptor

import (
	"strings"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
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

var providers = map[string]pb.Provider{
	events.ProviderAnthropic: pb.Provider_PROVIDER_ANTHROPIC,
	events.ProviderOpenAI:    pb.Provider_PROVIDER_OPENAI,
	events.ProviderGoogle:    pb.Provider_PROVIDER_GOOGLE,
}

var apis = map[string]pb.Api{
	events.APIAnthropicMessages: pb.Api_API_ANTHROPIC_MESSAGES,
	events.APIClaudeAIWeb:       pb.Api_API_CLAUDE_AI_WEB,
	events.APIOpenAIResponses:   pb.Api_API_OPENAI_RESPONSES,
	events.APIOpenAIChat:        pb.Api_API_OPENAI_CHAT_COMPLETIONS,
	events.APIChatGPTBackend:    pb.Api_API_CHATGPT_BACKEND,
	events.APIGemini:            pb.Api_API_GEMINI,
}

var optionKeys = map[string]pb.Option{
	events.OptionEffort:      pb.Option_OPTION_EFFORT,
	events.OptionSpeed:       pb.Option_OPTION_SPEED,
	events.OptionServiceTier: pb.Option_OPTION_SERVICE_TIER,
	events.OptionThinking:    pb.Option_OPTION_THINKING,
}

// options are a call's settings on the wire.
func options(os []events.Option) []*pb.OptionValue {
	var out []*pb.OptionValue
	for _, o := range os {
		if k, ok := optionKeys[o.Key]; ok {
			out = append(out, &pb.OptionValue{Key: k, Value: o.Value})
		}
	}
	return out
}

// Call builds the Call every callback carries, its identity resolved from
// what's known now (never waits).
func (ci *CallInfo) Call() *pb.Call { return ci.callWith(ci.who().identity()) }

func (ci *CallInfo) callWith(id *pb.Identity) *pb.Call {
	return &pb.Call{
		CallId: ci.CallID, Mode: mode(ci.Enforce), Transport: transport(ci.WebSocket),
		Host: ci.Host, Method: ci.Method, Path: ci.Path, StartUnixNano: ci.StartNS,
		Identity: id, Provider: providers[events.ProviderOf(ci.Host)], Api: apis[events.APIOf(ci.Host, ci.Path)],
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

// TurnStart builds OnTurnStart's message for a WebSocket's request (msg: its
// response.create, plain JSON) on the connection's call (host, path): turn is
// its number on the connection.
func TurnStart(call *pb.Call, host, path string, turn int, msg []byte) *pb.TurnStart {
	s := events.SummarizeRequest(host, path, nil, msg)
	return &pb.TurnStart{Call: own(call), WsTurn: int32(turn), Request: requestInfo(s, len(msg))}
}

// Start builds OnCallStart's message.
func (ci *CallInfo) Start() *pb.CallStart { return ci.startWith(ci.Call()) }

func (ci *CallInfo) startWith(call *pb.Call) *pb.CallStart {
	return &pb.CallStart{Call: call, Headers: ci.Headers, Request: requestInfo(ci.Summary, ci.RequestBytes)}
}

func requestInfo(s events.RequestSummary, bytes int) *pb.RequestInfo {
	ri := &pb.RequestInfo{
		Model: s.Model, Stream: s.Stream, MaxTokens: int32(s.MaxTokens),
		Prewarm: s.Prewarm, Attachments: int32(s.Attachments), RequestBytes: int64(bytes), Options: options(s.Options),
	}
	for _, t := range s.ToolsAvailable {
		ri.ToolsAvailable = append(ri.ToolsAvailable, t.Name)
		if t.Kind == "mcp" {
			ri.McpToolsAvailable = append(ri.McpToolsAvailable, t.Server+"/"+t.Tool)
		}
	}
	return ri
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
		Identity: id, Provider: providers[events.ProviderOf(e.Host)], Api: apis[events.APIOf(e.Host, e.Path)],
		Client: &pb.Client{Headers: e.Client},
	}
	if e.Client != nil {
		call.Client.UserAgent = e.Client["user-agent"]
		call.Client.Product = events.ProductOf(e.Host, e.Client["user-agent"], e.Client["originator"], e.SessionID)
	}
	req := &pb.RequestInfo{
		Model: e.Model, Stream: e.Stream, MaxTokens: int32(e.MaxTokens),
		Prewarm: e.Prewarm, RequestBytes: int64(e.RequestBytes), Options: options(e.RequestOptions()),
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
		ResponseBytes: int64(e.ResponseBytes), Error: e.Error, Options: options(e.ResponseOptions()),
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
