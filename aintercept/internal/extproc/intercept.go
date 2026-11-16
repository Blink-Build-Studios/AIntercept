package extproc

import (
	"context"
	"strings"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// The customer Interceptor (docs/INTERCEPTOR.md), observe side: callbacks
// queued off the stream, never awaited. The enforce side is enforce.go.
//
// With contract v2 the content callback is OnRequest: each model request's
// new messages (HTTP bodies and Codex's WebSocket response.create), parsed
// by convo. Responses aren't sent; in enforce, their tool calls go to
// OnToolCall (respond.go).

// interceptState is a stream's Interceptor bookkeeping, shared by both modes.
type interceptState struct {
	started bool
	call    *pb.Call
	seq     [2]int64 // messages sent, client→server and server→client
	// Observe, with content: the response's event stream cut into events, or
	// its whole body (capped) when it isn't one.
	sse       sseSplitter
	resp      []byte
	respTrunc bool
	respSSE   bool
	respKnown bool
	// v2 enforce: the request's format, when its reply's tool calls are held.
	holdTools string
}

// interceptContentCap bounds a whole body sent to OnMessage in observe mode.
const interceptContentCap = 4 << 20

// intercepting reports whether this stream's calls go to an Interceptor.
func (srv *Server) intercepting(st *streamState) bool {
	return srv.ic != nil && !st.synthetic
}

func (st *streamState) icState() *interceptState {
	if st.ic == nil {
		st.ic = &interceptState{}
	}
	return st.ic
}

// callInfo describes the call for OnCallStart, parsing the request body.
func (srv *Server) callInfo(st *streamState, body []byte) *interceptor.CallInfo {
	return &interceptor.CallInfo{
		CallID: st.correlationID, Enforce: st.enforce, WebSocket: st.wsRequested,
		Host: st.host, Method: st.method, Path: st.path, StartNS: st.reqStartNS,
		KeyHash: st.keyHash, KeyHint: st.keyHint, IP: st.ip, Auth: st.auth,
		Headers: st.reqHeaders, Summary: events.SummarizeRequest(st.host, st.path, st.reqHeaders, body),
		RequestBytes: len(body),
	}
}

// message builds an OnMessage message for the stream, numbering it.
func (srv *Server) message(st *streamState, dir events.Direction, kind pb.MessageKind, payload []byte, contentType, sseEvent string) *pb.Message {
	ic := st.icState()
	i := 0
	if dir == events.DirOutbound {
		i = 1
	}
	ic.seq[i]++
	return interceptor.Message(ic.call, dir, ic.seq[i], kind, payload, contentType, sseEvent)
}

// observeStart queues OnCallStart (and, with content, the request body) once
// the request has arrived: at its headers when it has no body or is a
// WebSocket upgrade, else at the end of its body.
func (srv *Server) observeStart(st *streamState) {
	if !srv.intercepting(st) || st.enforce || st.icState().started {
		return
	}
	ic := st.icState()
	ic.started = true
	var body []byte
	if !st.reqTrunc {
		body = st.reqBuf
	}
	ci := srv.callInfo(st, body)
	ic.call = ci.Call()
	srv.ic.ObserveStart(ci.Start())
	if srv.ic.Content() && srv.ic.V2() && st.reqTrunc {
		srv.ic.Unparsed() // over the buffer: it can't be read
	}
	if srv.ic.Content() && len(body) > 0 {
		if plain, err := events.DecodeBody(body, st.reqHeaders["content-encoding"]); err == nil {
			if !srv.ic.V2() {
				srv.ic.ObserveMessage(srv.message(st, events.DirInbound, pb.MessageKind_MESSAGE_KIND_HTTP_REQUEST_BODY, plain, st.reqHeaders["content-type"], ""))
			} else if r, ok := srv.parseRequest(st, plain); ok {
				srv.ic.ObserveRequest(ic.call, r)
			}
		}
	}
}

// parseRequest reads a request body for OnRequest (v2). A body it can't
// read goes upstream untouched, and is counted.
func (srv *Server) parseRequest(st *streamState, plain []byte) (convo.Request, bool) {
	r, ok := srv.ic.Parser().Parse(st.host, st.path, plain)
	if !ok {
		srv.ic.Unparsed()
	}
	return r, ok
}

// parseWSRequest reads a client WebSocket message for OnRequest (v2): a
// response.create. Other messages aren't requests; a response.create it
// can't read is counted.
func (srv *Server) parseWSRequest(st *streamState, m wsframe.Message) (convo.Request, bool) {
	if m.Opcode != wsframe.OpText {
		return convo.Request{}, false
	}
	r, ok := srv.ic.Parser().ParseWebSocket(st.host, st.path, m.Payload)
	if !ok && messageType(m.Payload) == "response.create" {
		srv.ic.Unparsed()
	}
	return r, ok
}

// observeResponseContent hands an HTTP response's content to OnMessage
// (observe mode): each event of an event stream as it completes, any other
// body whole at its end.
func (srv *Server) observeResponseContent(st *streamState, chunk []byte, eos bool) {
	if !srv.intercepting(st) || st.enforce || !srv.ic.Content() || srv.ic.V2() || st.ic == nil || !st.ic.started {
		return
	}
	ic := st.ic
	if !ic.respKnown {
		ic.respKnown = true
		ic.respSSE = strings.Contains(st.respHeaders["content-type"], "text/event-stream") && st.respEncoding == ""
	}
	if ic.respSSE {
		for _, ev := range ic.sse.feed(chunk) {
			srv.ic.ObserveMessage(srv.message(st, events.DirOutbound, pb.MessageKind_MESSAGE_KIND_SSE_EVENT, ev.data, "text/event-stream", ev.name))
		}
		return
	}
	ic.resp, ic.respTrunc = appendCapped(ic.resp, chunk, ic.respTrunc, interceptContentCap)
	if eos {
		if !ic.respTrunc {
			if plain, err := events.DecodeBody(ic.resp, st.respEncoding); err == nil && len(plain) > 0 {
				srv.ic.ObserveMessage(srv.message(st, events.DirOutbound, pb.MessageKind_MESSAGE_KIND_HTTP_RESPONSE_BODY, plain, st.respHeaders["content-type"], ""))
			}
		}
		ic.resp = nil
	}
}

// observeWSMessage hands a WebSocket data message to OnMessage (observe
// mode), or with v2 a client's response.create to OnRequest.
func (srv *Server) observeWSMessage(st *streamState, dir events.Direction, m wsframe.Message) {
	if !srv.intercepting(st) || st.enforce || !srv.ic.Content() || st.ic == nil || !st.ic.started {
		return
	}
	if !srv.ic.V2() {
		srv.ic.ObserveMessage(srv.message(st, dir, wsKind(m.Opcode), m.Payload, "", ""))
		return
	}
	if dir == events.DirInbound {
		if r, ok := srv.parseWSRequest(st, m); ok {
			srv.ic.ObserveRequest(st.ic.call, r)
		}
	}
}

func wsKind(op byte) pb.MessageKind {
	if op == 0x2 {
		return pb.MessageKind_MESSAGE_KIND_WS_BINARY
	}
	return pb.MessageKind_MESSAGE_KIND_WS_TEXT
}

// decideStart is OnCallStart in enforce mode (Allow without an Interceptor).
func (srv *Server) decideStart(st *streamState, ci *interceptor.CallInfo) interceptor.Decision {
	ic := st.icState()
	ic.started = true
	ic.call = ci.Call()
	if !srv.intercepting(st) {
		return interceptor.Decision{Action: interceptor.Allow}
	}
	return srv.ic.DecideStart(context.Background(), ci.Start())
}

// decideMessage is OnMessage in enforce mode (Allow without content, and
// with v2).
func (srv *Server) decideMessage(st *streamState, dir events.Direction, kind pb.MessageKind, payload []byte, contentType, sseEvent string) interceptor.Decision {
	if !srv.intercepting(st) || !srv.ic.Content() || srv.ic.V2() {
		return interceptor.Decision{Action: interceptor.Allow}
	}
	return srv.ic.DecideMessage(context.Background(), srv.message(st, dir, kind, payload, contentType, sseEvent))
}

// decideRequest is OnRequest in enforce mode (v2).
func (srv *Server) decideRequest(st *streamState, r convo.Request) interceptor.Decision {
	return srv.ic.DecideRequest(context.Background(), st.icState().call, r)
}

// enforcingContent reports whether an enforced stream evaluates content as
// v1 does: bodies, events and WebSocket messages, both ways.
func (srv *Server) enforcingContent(st *streamState) bool {
	return st.enforce && srv.intercepting(st) && srv.ic.Content() && !srv.ic.V2()
}

// enforcingRequests reports whether an enforced stream evaluates its
// requests' new messages (v2). Responses pass untouched.
func (srv *Server) enforcingRequests(st *streamState) bool {
	return st.enforce && srv.intercepting(st) && srv.ic.Content() && srv.ic.V2()
}
