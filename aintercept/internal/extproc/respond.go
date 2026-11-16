package extproc

import (
	"bytes"
	"context"
	"strings"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// Contract v2 in enforce (docs/INTERCEPTOR_V2.md): Respond, and every
// message of the reply decided before the client sees it.
//
//   - Respond (OnCallStart, OnRequest): when the client keeps the
//     conversation, the reply is synthesized in the request's format and the
//     call never goes upstream (an immediate 200, or a WebSocket turn's
//     events); when the provider keeps it (a chained request), the request is
//     rewritten to ask the model for exactly the text.
//   - The reply (Anthropic Messages, Responses over HTTP or Codex's
//     WebSocket, Chat Completions): its text is held piece by piece and
//     decided by OnReply, each tool call held from its first event until
//     it's complete and decided by OnToolCall (convo.Stream). A whole JSON
//     reply is decided before it's released.

// readRequest decodes and parses an HTTP request body.
func (srv *Server) readRequest(st *streamState, body []byte) (convo.Request, bool) {
	plain, err := events.DecodeBody(body, st.reqHeaders["content-encoding"])
	if err != nil {
		return convo.Request{}, false
	}
	return srv.ic.Parser().Parse(st.host, st.path, plain)
}

// respondHTTP delivers a Respond on an HTTP call (r: its parsed request, if
// ok). Synthesized: the immediate reply to send. Rewritten: the body to
// forward. Neither: it couldn't be delivered (an unreadable request, nothing
// to rewrite), and the call goes on as sent.
func (srv *Server) respondHTTP(st *streamState, ci *interceptor.CallInfo, r convo.Request, ok bool, d interceptor.Decision) ([]*extprocv3.ProcessingResponse, []byte) {
	switch {
	case !ok:
	case r.Chained:
		if b, err := r.Rewrite(d.Text); err == nil {
			srv.ic.Responded(st.correlationID, d, "rewritten")
			return nil, b
		}
	default:
		ct, body := convo.Synthesize(r.Format, ci.Summary.Model, d.Text, ci.Summary.Stream)
		srv.ic.Responded(st.correlationID, d, "synthesized")
		return srv.synthesized(st, ct, body), nil
	}
	srv.ic.Responded(st.correlationID, d, "")
	return nil, nil
}

// synthesized answers the call with a reply that never went upstream, and
// records it as the call's response.
func (srv *Server) synthesized(st *streamState, contentType string, body []byte) []*extprocv3.ProcessingResponse {
	st.enf.ended = true
	st.status, st.respStartNS = 200, time.Now().UnixNano()
	st.respHeaders = map[string]string{":status": "200", "content-type": contentType}
	st.respBytes = len(body)
	st.respBuf, st.respTrunc = appendCapped(nil, body, false, respBufferCap)
	srv.account(st)
	srv.emitComplete(st)
	return []*extprocv3.ProcessingResponse{{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{ImmediateResponse: &extprocv3.ImmediateResponse{
			Status: &typev3.HttpStatus{Code: typev3.StatusCode_OK},
			Headers: &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{{
				Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte(contentType)},
			}}},
			Body:    body,
			Details: "aintercept_interceptor_respond",
		}},
	}}
}

// respondWS delivers a Respond to a Codex WebSocket's response.create: the
// decision for the message (Respond: it never goes upstream; Replace: the
// rewritten request; Allow: as sent) and the reply's frames for the client.
func (srv *Server) respondWS(st *streamState, r convo.Request, create []byte, d interceptor.Decision) (interceptor.Decision, []byte) {
	if r.Chained {
		b, err := r.Rewrite(d.Text)
		if err != nil {
			srv.ic.Responded(st.correlationID, d, "")
			return interceptor.Decision{Action: interceptor.Allow}, nil
		}
		srv.ic.Responded(st.correlationID, d, "rewritten")
		return interceptor.Decision{Action: interceptor.Replace, Payload: b}, nil
	}
	srv.ic.Responded(st.correlationID, d, "synthesized")
	var out []byte
	for _, ev := range convo.SynthesizeEvents(events.ModelFromRequestBody(create), d.Text) {
		srv.wsMessage(st, events.DirOutbound, wsframe.Message{Opcode: wsframe.OpText, Payload: ev}) // records the turn
		out = append(out, wsframe.Encode(wsframe.OpText, ev, false)...)
	}
	st.ws.conn.BytesDown += len(out)
	return d, out
}

// ── The reply, message by message ────────────────────────────────────────────

// holdsReplies reports whether replies go to the Interceptor: their text to
// OnReply or their tool calls to OnToolCall.
func (srv *Server) holdsReplies() bool { return srv.ic.Replies() || srv.ic.ToolCalls() }

// holdsReply reports whether a reply to a request in format goes to the
// Interceptor.
func (srv *Server) holdsReply(format string) bool {
	return srv.holdsReplies() && (format == convo.FormatAnthropic || format == convo.FormatResponses || format == convo.FormatChat)
}

// deciders ask OnReply and OnToolCall about the stream's reply (enforce).
func (srv *Server) deciders(st *streamState) convo.Deciders {
	call := st.icState().call
	var dec convo.Deciders
	dec.Opaque = func(id string) { srv.ic.Opaque(context.Background(), call, id) }
	if srv.ic.ToolCalls() {
		dec.ToolCall = func(tc convo.ToolCall) convo.Answer {
			return srv.answer(st, srv.ic.DecideToolCall(context.Background(), call, tc))
		}
	}
	if srv.ic.Replies() {
		dec.Text = func(p convo.TextPiece) convo.Answer {
			return srv.answer(st, srv.ic.DecideReply(context.Background(), call, p))
		}
	}
	return dec
}

// answer turns a decision on part of the reply into the stream's answer.
func (srv *Server) answer(st *streamState, d interceptor.Decision) convo.Answer {
	switch d.Action {
	case interceptor.Replace:
		return convo.Answer{Verdict: convo.Replace, Arguments: string(d.Payload), Text: d.Text, Keep: d.Keep}
	case interceptor.Respond:
		return convo.Answer{Verdict: convo.Respond, Text: d.Text}
	case interceptor.Hold:
		return convo.Answer{Verdict: convo.Hold}
	case interceptor.Deny:
		st.enf.toolDeny = &d
		return convo.Answer{Verdict: convo.Stop}
	}
	return convo.Answer{Keep: d.Keep}
}

// holdReplyHeaders starts holding a successful reply: event by event for an
// event stream, whole for JSON. Anything else passes.
func (srv *Server) holdReplyHeaders(st *streamState) []*extprocv3.ProcessingResponse {
	e := st.enf
	ct := st.respHeaders["content-type"]
	switch {
	case st.status != 200:
	case st.respEncoding != "":
		srv.undecodable(st, "reply with content-encoding "+st.respEncoding)
	case strings.Contains(ct, "text/event-stream"):
		e.mode = respToolsSSE
		e.tools = convo.NewStream(st.ic.replyFormat, convo.SSE, srv.deciders(st))
		return []*extprocv3.ProcessingResponse{headersResponse(false, []string{"content-length"})}
	case strings.Contains(ct, "json"):
		e.mode, e.respHeld = respToolsWhole, true
		return nil
	}
	return []*extprocv3.ProcessingResponse{continueResponseHeaders()}
}

// toolsSSE releases what an event stream's chunk lets go: text and each
// tool call once decided. Its pending text is asked about when nothing more
// is waiting. A Respond or a refusal ends the reply.
func (srv *Server) toolsSSE(st *streamState, chunk []byte, eos bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	var out []byte
	evs := e.sse.feed(chunk)
	if !eos && !e.waiting() {
		evs = append(evs, sseEvent{}) // pump
	}
	for _, ev := range evs {
		if ev.raw == nil {
			out = append(out, e.tools.Pump()...)
		} else {
			out = append(out, e.tools.Feed(ev.name, ev.data, ev.raw)...)
		}
		if e.tools.Ended() {
			e.ended = true
			if d := e.toolDeny; d != nil {
				dn := srv.denial(st, *d)
				st.denyReason = "denied by interceptor: " + dn.message()
				out = append(out, dn.sseEvent()...)
			}
			return []*extprocv3.ProcessingResponse{bodyResponse(false, out, true)}
		}
	}
	if eos {
		out = append(append(out, e.tools.Flush()...), e.sse.rest()...)
	}
	return []*extprocv3.ProcessingResponse{bodyResponse(false, out, eos)}
}

// toolsWhole holds a JSON reply to its end, then decides its tool calls.
func (srv *Server) toolsWhole(st *streamState, chunk []byte, eos bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	if len(e.resp)+len(chunk) > enforceBodyCap {
		e.mode, e.respHeld = respPass, false
		srv.undecodable(st, "response body over the cap")
		held := append(e.resp, chunk...)
		e.resp = nil
		return []*extprocv3.ProcessingResponse{continueResponseHeaders(), bodyResponse(false, held, eos)}
	}
	e.resp = append(e.resp, chunk...)
	if !eos {
		return nil
	}
	e.respHeld = false
	body := e.resp
	e.resp = nil
	out, stopped, err := convo.DecideBody(st.ic.replyFormat, body, srv.deciders(st))
	switch {
	case stopped:
		return srv.deny(st, *e.toolDeny)
	case err != nil || bytes.Equal(out, body):
		return []*extprocv3.ProcessingResponse{continueResponseHeaders(), bodyResponse(false, body, true)}
	}
	return []*extprocv3.ProcessingResponse{
		headersResponse(false, []string{"content-length", "content-encoding"}),
		bodyResponse(false, out, true),
	}
}

// wsToolCalls forwards a Codex WebSocket's server messages, re-framed: each
// turn's text and tool calls held and decided, the rest as it came. After a
// Respond the turn's remaining messages are dropped; a refusal ends the
// connection. The turn's pending text is asked about when nothing more is
// waiting.
func (srv *Server) wsToolCalls(st *streamState, msgs []wsframe.Message, eos bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	var out []byte
	if !e.waiting() {
		msgs = append(msgs, wsframe.Message{Opcode: pumpOp})
	}
	for _, m := range msgs {
		if m.Opcode == pumpOp {
			if e.wsTools == nil || e.wsTools.Ended() {
				continue
			}
			out = append(out, e.wsTools.Pump()...)
			if e.wsTools.Ended() && e.toolDeny != nil {
				return append([]*extprocv3.ProcessingResponse{bodyResponse(false, out, false)}, srv.wsDeny(st, false, *e.toolDeny, 1008)...)
			}
			continue
		}
		srv.wsMessage(st, events.DirOutbound, m) // recording: turns and the connection row
		if m.Truncated {
			return srv.wsDeny(st, false, interceptor.Decision{Reason: "WebSocket message too large to evaluate."}, 1009)
		}
		frame := wsframe.Encode(m.Opcode, m.Payload, false)
		if m.Opcode != wsframe.OpText {
			out = append(out, frame...)
			continue
		}
		typ := messageType(m.Payload)
		if typ == "response.created" { // a new turn
			e.wsTools = convo.NewStream(convo.FormatResponses, wsText, srv.deciders(st))
		}
		switch {
		case e.wsTools == nil:
			out = append(out, frame...)
		case e.wsTools.Ended(): // the turn was answered: the rest of it is dropped
		default:
			out = append(out, e.wsTools.Feed(typ, m.Payload, frame)...)
			if e.wsTools.Ended() && e.toolDeny != nil {
				res := srv.wsDeny(st, false, *e.toolDeny, 1008)
				if len(out) > 0 {
					res = append([]*extprocv3.ProcessingResponse{bodyResponse(false, out, false)}, res...)
				}
				return res
			}
		}
	}
	return []*extprocv3.ProcessingResponse{bodyResponse(false, out, eos)}
}

// pumpOp marks the point in wsToolCalls' messages where the pending text is
// asked about (no WebSocket opcode).
const pumpOp = 0xff

// wsText frames an event the Stream makes as a server text message.
func wsText(_ string, data []byte) []byte { return wsframe.Encode(wsframe.OpText, data, false) }

func firstOf(a, b []byte) []byte {
	if a != nil {
		return a
	}
	return b
}
