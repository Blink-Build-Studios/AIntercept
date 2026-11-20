package extproc

import (
	"bytes"
	"context"
	"strings"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// The customer Interceptor (docs/INTERCEPTOR.md), observe side: callbacks
// queued off the stream, never awaited. The enforce side is enforce.go.
//
// The content callbacks are OnRequest, each model request's new messages
// (HTTP bodies and Codex's WebSocket response.create), parsed by convo, and
// for the reply OnReply (its text, piece by piece) and OnToolCall (each tool
// call): in path in enforce (respond.go), queued here in observe.

// interceptState is a stream's Interceptor bookkeeping, shared by both modes.
type interceptState struct {
	started bool
	call    *pb.Call
	// Observe: the reply's event stream cut into events.
	sse sseSplitter
	// The request's format, when its reply goes to OnReply and OnToolCall;
	// observe: the reply's stream, or its body held whole.
	replyFormat string
	reply       *convo.Stream
	replyBody   []byte
	replyTrunc  bool
	// replyKind is what the reply is (from its content-type, or sniffed from
	// its first bytes, held in sniff, when it has none); replyEnc holds an
	// encoded reply to its end, to decode it whole.
	replyKind     bodyKind
	sniff         []byte
	replyEnc      []byte
	replyEncTrunc bool
}

// interceptBodyCap bounds a body observe mode holds whole to read it for the
// Interceptor: a request, a JSON reply, an encoded reply. The same as
// enforce's.
const interceptBodyCap = enforceBodyCap

// bodyKind is what a reply's body is.
type bodyKind byte

const (
	kindUnknown bodyKind = iota // not yet known: no content-type, too few bytes to tell
	kindSSE                     // an event stream
	kindJSON                    // a whole JSON body
	kindOther                   // anything else: not read
)

// replyKindOf is what a reply's content-type says it is (kindUnknown without
// one).
func replyKindOf(contentType string) bodyKind {
	switch {
	case contentType == "":
		return kindUnknown
	case strings.Contains(contentType, "text/event-stream"):
		return kindSSE
	case strings.Contains(contentType, "json"):
		return kindJSON
	}
	return kindOther
}

// sniffCap bounds the bytes held to tell what a reply without a
// content-type is.
const sniffCap = 4 << 10

// sniffKind is what a reply without a content-type is, from its first bytes:
// an event stream when it begins with an SSE field (Codex's replies over HTTP
// carry no content-type), JSON when it begins with an object or array.
// kindUnknown while the bytes so far can't tell.
func sniffKind(b []byte) bodyKind {
	t := bytes.TrimLeft(b, " \t\r\n")
	switch {
	case len(t) == 0:
		return kindUnknown
	case t[0] == '{' || t[0] == '[':
		return kindJSON
	}
	fields := []string{"event:", "data:", "id:", "retry:", ":"}
	for _, f := range fields {
		if bytes.HasPrefix(t, []byte(f)) {
			return kindSSE
		}
	}
	for _, f := range fields {
		if len(t) < len(f) && strings.HasPrefix(f, string(t)) {
			return kindUnknown
		}
	}
	return kindOther
}

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
		RequestBytes: len(body), Identities: srv.ids, Lookup: st.lookup,
	}
}

// observeStart queues OnCallStart (and, with content, OnRequest) once
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
	ic.call = srv.ic.ObserveCall(srv.callInfo(st, body), srv.idWait)
	if srv.ic.Content() && st.reqTrunc {
		srv.ic.Unparsed("over the buffer") // it can't be read
	}
	if srv.ic.Content() && len(body) > 0 {
		if plain, err := events.DecodeBody(body, st.reqHeaders["content-encoding"]); err == nil {
			if r, ok := srv.parseRequest(st, plain); ok {
				srv.ic.ObserveRequest(ic.call, r)
				if srv.holdsReply(r.Format) {
					ic.replyFormat = r.Format
				}
			}
		}
	}
}

// parseRequest reads a request body for OnRequest. A body it can't
// read goes upstream untouched, and is counted.
func (srv *Server) parseRequest(st *streamState, plain []byte) (convo.Request, bool) {
	r, why := srv.ic.Parser().ParseReason(st.host, st.path, plain)
	if why != "" {
		srv.ic.Unparsed(why)
	}
	return r, why == ""
}

// parseWSRequest reads a client WebSocket message for OnRequest: a
// response.create. Other messages aren't requests; a response.create it
// can't read is counted.
func (srv *Server) parseWSRequest(st *streamState, m wsframe.Message) (convo.Request, bool) {
	if m.Opcode != wsframe.OpText {
		return convo.Request{}, false
	}
	r, ok := srv.ic.Parser().ParseWebSocket(st.host, st.path, m.Payload)
	if !ok && messageType(m.Payload) == "response.create" {
		srv.ic.Unparsed("not a conversation")
	}
	return r, ok
}

// observeResponseContent hands an HTTP response's reply to OnReply and
// OnToolCall (observe mode).
func (srv *Server) observeResponseContent(st *streamState, chunk []byte, eos bool) {
	if !srv.intercepting(st) || st.enforce || !srv.ic.Content() || st.ic == nil || !st.ic.started {
		return
	}
	srv.observeReply(st, chunk, eos)
}

// observeWSMessage hands a client's response.create on a WebSocket to
// OnRequest, and the server's reply to OnReply and OnToolCall (observe mode).
func (srv *Server) observeWSMessage(st *streamState, dir events.Direction, m wsframe.Message) {
	if !srv.intercepting(st) || st.enforce || !srv.ic.Content() || st.ic == nil || !st.ic.started {
		return
	}
	if dir == events.DirInbound {
		if r, ok := srv.parseWSRequest(st, m); ok {
			srv.ic.ObserveRequest(st.ic.call, r)
		}
		return
	}
	if m.Opcode != wsframe.OpText || !srv.holdsReplies() {
		return
	}
	ic := st.ic
	typ := messageType(m.Payload)
	if typ == "response.created" { // a new turn
		ic.reply = convo.NewStream(convo.FormatResponses, wsText, srv.observers(st))
	}
	if ic.reply != nil {
		ic.reply.Feed(typ, m.Payload, nil)
		ic.reply.Pump()
	}
}

// observers queue OnReply and OnToolCall for a reply (observe mode): the
// Stream carries on as if each had accepted.
func (srv *Server) observers(st *streamState) convo.Deciders {
	call := st.ic.call
	return convo.Deciders{
		Opaque: func(id string) { srv.ic.ObserveOpaque(call, id) },
		ToolCall: func(tc convo.ToolCall) convo.Answer {
			srv.ic.ObserveToolCall(call, tc)
			return convo.Answer{}
		},
		Text: func(p convo.TextPiece) convo.Answer {
			srv.ic.ObserveReply(call, p)
			return convo.Answer{}
		},
	}
}

// observeReply follows a reply in observe mode. Observe can't ask for
// a plain reply, so an encoded one is held to its end and decoded whole, as
// the extractors do; a plain one is read as it comes.
func (srv *Server) observeReply(st *streamState, chunk []byte, eos bool) {
	ic := st.ic
	if ic.replyFormat == "" || st.status != 200 {
		return
	}
	enc := st.respEncoding
	if enc == "" {
		srv.followReply(st, chunk, eos)
		return
	}
	ic.replyEnc, ic.replyEncTrunc = appendCapped(ic.replyEnc, chunk, ic.replyEncTrunc, interceptBodyCap)
	if !eos {
		return
	}
	body, trunc := ic.replyEnc, ic.replyEncTrunc
	ic.replyEnc = nil
	if trunc {
		srv.ic.Unparsed("reply over the buffer")
		return
	}
	plain, err := events.DecodeBody(body, enc)
	if err != nil {
		srv.ic.Unparsed("undecodable reply")
		return
	}
	srv.followReply(st, plain, true)
}

// followReply reads a plain reply (observe): an event stream event by event,
// asking about its text as each chunk ends; a JSON body whole at its end. A
// reply without a content-type is sniffed from its first bytes.
func (srv *Server) followReply(st *streamState, chunk []byte, eos bool) {
	ic := st.ic
	if ic.replyKind == kindUnknown {
		if k := replyKindOf(st.respHeaders["content-type"]); k != kindUnknown {
			ic.replyKind = k
		} else {
			ic.sniff = append(ic.sniff, chunk...)
			k := sniffKind(ic.sniff)
			if k == kindUnknown && !eos && len(ic.sniff) <= sniffCap {
				return
			}
			if k == kindUnknown {
				k = kindOther
			}
			ic.replyKind, chunk, ic.sniff = k, ic.sniff, nil
		}
	}
	switch ic.replyKind {
	case kindSSE:
		if ic.reply == nil {
			ic.reply = convo.NewStream(ic.replyFormat, convo.SSE, srv.observers(st))
		}
		for _, ev := range ic.sse.feed(chunk) {
			ic.reply.Feed(ev.name, ev.data, ev.raw)
		}
		ic.reply.Pump()
		if eos {
			ic.reply.Flush()
		}
	case kindJSON:
		ic.replyBody, ic.replyTrunc = appendCapped(ic.replyBody, chunk, ic.replyTrunc, interceptBodyCap)
		if eos && ic.replyTrunc {
			srv.ic.Unparsed("reply over the buffer")
		} else if eos {
			_, _, _ = convo.DecideBody(ic.replyFormat, ic.replyBody, srv.observers(st))
		}
		if eos {
			ic.replyBody = nil
		}
	}
}

// decideStart is OnCallStart in enforce mode (Allow without an Interceptor).
func (srv *Server) decideStart(st *streamState, ci *interceptor.CallInfo) interceptor.Decision {
	ic := st.icState()
	ic.started = true
	if !srv.intercepting(st) {
		ic.call = ci.Call()
		return interceptor.Decision{Action: interceptor.Allow}
	}
	var d interceptor.Decision
	ic.call, d = srv.ic.DecideCall(context.Background(), ci, srv.enforceIDWait)
	return d
}

// decideRequest is OnRequest in enforce mode.
func (srv *Server) decideRequest(st *streamState, r convo.Request) interceptor.Decision {
	return srv.ic.DecideRequest(context.Background(), st.icState().call, r)
}

// enforcingTurns reports whether an enforced WebSocket's requests go to
// OnTurnStart (its client messages decoded and re-framed for it).
func (srv *Server) enforcingTurns(st *streamState) bool {
	return st.enforce && srv.intercepting(st) && srv.ic.Turns()
}

// observeTurn queues OnTurnStart for a WebSocket's request (observe mode):
// turn is its number on the connection.
func (srv *Server) observeTurn(st *streamState, turn int, msg []byte) {
	if !srv.intercepting(st) || st.enforce || st.ic == nil || !st.ic.started {
		return
	}
	srv.ic.ObserveTurn(interceptor.TurnStart(st.ic.call, st.host, st.path, turn, msg))
}

// enforcingRequests reports whether an enforced stream evaluates its
// requests' new messages (and, when the Interceptor has rules on them, its
// reply's text and tool calls).
func (srv *Server) enforcingRequests(st *streamState) bool {
	return st.enforce && srv.intercepting(st) && srv.ic.Content()
}
