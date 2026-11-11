package extproc

import (
	"context"
	"strings"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
)

// The customer Interceptor (docs/INTERCEPTOR.md), observe side: callbacks
// queued off the stream, never awaited. The enforce side is enforce.go.

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
	if srv.ic.Content() && len(body) > 0 {
		if plain, err := events.DecodeBody(body, st.reqHeaders["content-encoding"]); err == nil {
			srv.ic.ObserveMessage(srv.message(st, events.DirInbound, pb.MessageKind_MESSAGE_KIND_HTTP_REQUEST_BODY, plain, st.reqHeaders["content-type"], ""))
		}
	}
}

// observeResponseContent hands an HTTP response's content to OnMessage
// (observe mode): each event of an event stream as it completes, any other
// body whole at its end.
func (srv *Server) observeResponseContent(st *streamState, chunk []byte, eos bool) {
	if !srv.intercepting(st) || st.enforce || !srv.ic.Content() || st.ic == nil || !st.ic.started {
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

// observeWSMessage hands a WebSocket data message to OnMessage (observe mode).
func (srv *Server) observeWSMessage(st *streamState, dir events.Direction, op byte, payload []byte) {
	if !srv.intercepting(st) || st.enforce || !srv.ic.Content() || st.ic == nil || !st.ic.started {
		return
	}
	srv.ic.ObserveMessage(srv.message(st, dir, wsKind(op), payload, "", ""))
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

// decideMessage is OnMessage in enforce mode (Allow without content).
func (srv *Server) decideMessage(st *streamState, dir events.Direction, kind pb.MessageKind, payload []byte, contentType, sseEvent string) interceptor.Decision {
	if !srv.intercepting(st) || !srv.ic.Content() {
		return interceptor.Decision{Action: interceptor.Allow}
	}
	return srv.ic.DecideMessage(context.Background(), srv.message(st, dir, kind, payload, contentType, sseEvent))
}

// enforcingContent reports whether an enforced stream evaluates content.
func (srv *Server) enforcingContent(st *streamState) bool {
	return st.enforce && srv.intercepting(st) && srv.ic.Content()
}
