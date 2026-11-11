package extproc

import (
	"io"
	"log/slog"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// Enforce mode: streams from the enforcing ext_proc filter, in Envoy's
// FULL_DUPLEX_STREAMED body mode. Envoy forwards only what this server sends
// back, so every byte goes through here:
//
//   - Request headers are held until the whole request body has arrived, then
//     OnCallStart decides (and, with Interceptor content, OnMessage gets the
//     body). Allowed requests go upstream unchanged unless a verdict replaced
//     the body; denied ones get the provider's own error.
//   - Responses: without content, chunks pass straight through. With content,
//     an event stream is evaluated event by event as it streams, any other
//     body whole before it's released, and a WebSocket message by message.
//     With content on, enforce asks upstream for uncompressed responses and
//     no WebSocket compression, so verdicts never need re-compressing.
//
// Recording is unchanged: the same observation events are emitted as in
// observe mode, plus each verdict.

// enforceBodyCap bounds a request or whole response body held for a verdict.
// A larger request is refused (413): it can't be evaluated or released.
const enforceBodyCap = 32 << 20

// wsEnforceCap bounds one WebSocket message held for a verdict.
const wsEnforceCap = 16 << 20

type respMode int

const (
	respPass  respMode = iota // forward chunks as they arrive
	respSSE                   // evaluate event by event
	respWhole                 // evaluate the whole body before releasing it
)

type enforceState struct {
	reqBody     []byte
	reqTooBig   bool
	headersHeld bool
	reqDone     bool
	denied      bool
	mode        respMode
	resp        []byte
	respHeld    bool
	sse         sseSplitter
	// WebSocket with content: decoders per direction (uncompressed frames).
	wsUp, wsDown *wsframe.Decoder
	wsContent    bool
}

// processEnforce serves an enforced stream, starting with its first request.
func (srv *Server) processEnforce(stream extprocv3.ExternalProcessor_ProcessServer, st *streamState, req *extprocv3.ProcessingRequest) error {
	st.enforce = true
	st.enf = &enforceState{}
	for {
		for _, r := range srv.enforceHandle(st, req) {
			if err := stream.Send(r); err != nil {
				srv.wsClose(st)
				return err
			}
		}
		var err error
		req, err = stream.Recv()
		if err == io.EOF {
			srv.wsClose(st)
			return nil
		}
		if err != nil {
			srv.wsClose(st)
			srv.offer(st, events.Event{Phase: events.PhaseDisconnect, Error: err.Error()})
			return err
		}
	}
}

func (srv *Server) enforceHandle(st *streamState, req *extprocv3.ProcessingRequest) []*extprocv3.ProcessingResponse {
	e := st.enf
	switch v := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		st.ip = peerAddress(req)
		srv.observeHeaders(st, events.DirInbound, events.PhaseRequestHeaders, v.RequestHeaders)
		if v.RequestHeaders.GetEndOfStream() || st.wsRequested {
			e.reqDone = true
			return srv.enforceStart(st, nil, false)
		}
		e.headersHeld = true
		return nil

	case *extprocv3.ProcessingRequest_RequestBody:
		b := v.RequestBody
		srv.observeBody(st, events.DirInbound, events.PhaseRequestBodyChunk, b)
		if st.ws != nil {
			return srv.enforceWS(st, events.DirInbound, b)
		}
		if e.denied {
			return nil
		}
		if e.reqDone {
			// Already released (a WebSocket upgrade's bytes before the 101):
			// in full duplex Envoy forwards only what comes back, so pass them on.
			return []*extprocv3.ProcessingResponse{bodyResponse(true, b.GetBody(), b.GetEndOfStream())}
		}
		if !e.reqTooBig {
			if len(e.reqBody)+len(b.GetBody()) > enforceBodyCap {
				e.reqTooBig, e.reqBody = true, nil
			} else {
				e.reqBody = append(e.reqBody, b.GetBody()...)
			}
		}
		if !b.GetEndOfStream() {
			return nil
		}
		e.reqDone = true
		return srv.enforceStart(st, e.reqBody, true)

	case *extprocv3.ProcessingRequest_RequestTrailers:
		var out []*extprocv3.ProcessingResponse
		if e.headersHeld && !e.reqDone && !e.denied {
			e.reqDone = true
			out = srv.enforceStart(st, e.reqBody, true)
		}
		return append(out, continueRequestTrailers())

	case *extprocv3.ProcessingRequest_ResponseHeaders:
		h := v.ResponseHeaders
		srv.observeHeaders(st, events.DirOutbound, events.PhaseResponseHeaders, h)
		if e.denied {
			return nil
		}
		return srv.enforceResponseHeaders(st, h)

	case *extprocv3.ProcessingRequest_ResponseBody:
		b := v.ResponseBody
		var out []*extprocv3.ProcessingResponse
		switch {
		case e.denied:
		case st.ws != nil:
			out = srv.enforceWS(st, events.DirOutbound, b)
		default:
			// Decide before recording: the chunk that ends the response emits the
			// call's record, which carries this chunk's verdicts.
			out = srv.enforceResponseBody(st, b.GetBody(), b.GetEndOfStream())
		}
		srv.observeBody(st, events.DirOutbound, events.PhaseResponseBodyChunk, b)
		return out

	case *extprocv3.ProcessingRequest_ResponseTrailers:
		var out []*extprocv3.ProcessingResponse
		if e.respHeld && !e.denied {
			out = srv.enforceResponseBody(st, nil, true)
		}
		srv.emitComplete(st)
		return append(out, continueResponseTrailers())
	}
	return nil
}

// enforceStart decides the call (OnCallStart, then OnMessage on the body with
// content) and releases or refuses the request. hasBody: the request had a
// body (held in body; nil when it was too large to hold).
func (srv *Server) enforceStart(st *streamState, body []byte, hasBody bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	if hasBody && e.reqTooBig {
		return srv.deny(st, interceptor.Decision{Action: interceptor.Deny, Reason: "Request too large to evaluate.", HTTPStatus: 413})
	}
	ci := srv.callInfo(st, body)
	if d := srv.decideStart(st, ci); d.Action == interceptor.Deny {
		return srv.deny(st, d)
	}
	content := srv.enforcingContent(st)
	var remove []string
	if content {
		// Ask for responses the observer can read and release without
		// re-compressing them.
		if st.wsRequested {
			remove = append(remove, "sec-websocket-extensions")
		} else {
			remove = append(remove, "accept-encoding")
		}
	}
	if !hasBody {
		return []*extprocv3.ProcessingResponse{headersResponse(true, remove)}
	}
	fwd := body
	if content && len(body) > 0 {
		plain, err := events.DecodeBody(body, st.reqHeaders["content-encoding"])
		if err != nil {
			srv.undecodable(st, "request body: "+err.Error())
		} else {
			d := srv.decideMessage(st, events.DirInbound, pb.MessageKind_MESSAGE_KIND_HTTP_REQUEST_BODY, plain, st.reqHeaders["content-type"], "")
			switch d.Action {
			case interceptor.Replace:
				fwd = d.Payload
				remove = append(remove, "content-length", "content-encoding")
			case interceptor.Drop, interceptor.Deny:
				return srv.deny(st, d)
			}
		}
	}
	return []*extprocv3.ProcessingResponse{headersResponse(true, remove), bodyResponse(true, fwd, true)}
}

func (srv *Server) enforceResponseHeaders(st *streamState, h *extprocv3.HttpHeaders) []*extprocv3.ProcessingResponse {
	e := st.enf
	if st.ws != nil {
		if srv.enforcingContent(st) {
			if st.ws.ext.Deflate {
				// The server negotiated compression the client was never offered:
				// messages can't be re-encoded, so they pass unevaluated.
				srv.undecodable(st, "websocket: compression negotiated")
			} else {
				e.wsContent = true
				e.wsUp = wsframe.NewDecoder(false, false, wsEnforceCap)
				e.wsDown = wsframe.NewDecoder(false, false, wsEnforceCap)
			}
		}
		return []*extprocv3.ProcessingResponse{continueResponseHeaders()}
	}
	if !srv.enforcingContent(st) || h.GetEndOfStream() {
		return []*extprocv3.ProcessingResponse{continueResponseHeaders()}
	}
	if strings.Contains(st.respHeaders["content-type"], "text/event-stream") {
		if st.respEncoding != "" {
			srv.undecodable(st, "event stream with content-encoding "+st.respEncoding)
			return []*extprocv3.ProcessingResponse{continueResponseHeaders()}
		}
		e.mode = respSSE
		return []*extprocv3.ProcessingResponse{headersResponse(false, []string{"content-length"})}
	}
	e.mode, e.respHeld = respWhole, true
	return nil
}

func (srv *Server) enforceResponseBody(st *streamState, chunk []byte, eos bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	switch e.mode {
	case respSSE:
		var out []byte
		for _, ev := range e.sse.feed(chunk) {
			d := srv.decideMessage(st, events.DirOutbound, pb.MessageKind_MESSAGE_KIND_SSE_EVENT, ev.data, "text/event-stream", ev.name)
			switch d.Action {
			case interceptor.Replace:
				out = append(out, ev.withData(d.Payload)...)
			case interceptor.Drop:
			case interceptor.Deny:
				e.denied = true
				dn := srv.denial(st, d)
				st.denyReason = "denied by interceptor: " + dn.message()
				out = append(out, dn.sseEvent()...)
				return []*extprocv3.ProcessingResponse{bodyResponse(false, out, true)}
			default:
				out = append(out, ev.raw...)
			}
		}
		if eos {
			out = append(out, e.sse.rest()...)
		}
		// Answer every chunk, even with nothing: Envoy holds the response
		// headers until the first body response.
		return []*extprocv3.ProcessingResponse{bodyResponse(false, out, eos)}

	case respWhole:
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
		plain, err := events.DecodeBody(body, st.respEncoding)
		if err != nil {
			srv.undecodable(st, "response body: "+err.Error())
			return []*extprocv3.ProcessingResponse{continueResponseHeaders(), bodyResponse(false, body, true)}
		}
		d := srv.decideMessage(st, events.DirOutbound, pb.MessageKind_MESSAGE_KIND_HTTP_RESPONSE_BODY, plain, st.respHeaders["content-type"], "")
		switch d.Action {
		case interceptor.Replace:
			return []*extprocv3.ProcessingResponse{
				headersResponse(false, []string{"content-length", "content-encoding"}),
				bodyResponse(false, d.Payload, true),
			}
		case interceptor.Drop, interceptor.Deny:
			return srv.deny(st, d)
		}
		return []*extprocv3.ProcessingResponse{continueResponseHeaders(), bodyResponse(false, body, true)}
	}
	return []*extprocv3.ProcessingResponse{bodyResponse(false, chunk, eos)}
}

// enforceWS forwards a WebSocket body chunk: as is without content; with
// content, message by message through OnMessage, re-framed.
func (srv *Server) enforceWS(st *streamState, dir events.Direction, b *extprocv3.HttpBody) []*extprocv3.ProcessingResponse {
	e := st.enf
	toServer := dir == events.DirInbound
	if !e.wsContent {
		return []*extprocv3.ProcessingResponse{bodyResponse(toServer, b.GetBody(), b.GetEndOfStream())}
	}
	dec := e.wsDown
	if toServer {
		dec = e.wsUp
	}
	msgs, err := dec.Feed(b.GetBody())
	if err != nil {
		return srv.wsDeny(st, toServer, interceptor.Decision{Reason: "Undecodable WebSocket frame."}, 1002)
	}
	var out []byte
	for _, m := range msgs {
		srv.wsMessage(st, dir, m) // recording: turns and the connection row
		if m.Opcode >= 0x8 {
			out = append(out, wsframe.Encode(m.Opcode, m.Payload, toServer)...)
			continue
		}
		if m.Truncated {
			return srv.wsDeny(st, toServer, interceptor.Decision{Reason: "WebSocket message too large to evaluate."}, 1009)
		}
		d := srv.decideMessage(st, dir, wsKind(m.Opcode), m.Payload, "", "")
		switch d.Action {
		case interceptor.Replace:
			out = append(out, wsframe.Encode(m.Opcode, d.Payload, toServer)...)
		case interceptor.Drop:
		case interceptor.Deny:
			if len(out) > 0 {
				return append([]*extprocv3.ProcessingResponse{bodyResponse(toServer, out, false)}, srv.wsDeny(st, toServer, d, 1008)...)
			}
			return srv.wsDeny(st, toServer, d, 1008)
		default:
			out = append(out, wsframe.Encode(m.Opcode, m.Payload, toServer)...)
		}
	}
	// Answered even when empty (Envoy holds the 101 until a body response).
	return []*extprocv3.ProcessingResponse{bodyResponse(toServer, out, b.GetEndOfStream())}
}

// wsDeny ends a WebSocket: the client gets the provider's error message and a
// close frame, the server a close frame.
func (srv *Server) wsDeny(st *streamState, toServer bool, d interceptor.Decision, code int) []*extprocv3.ProcessingResponse {
	st.enf.denied = true
	dn := srv.denial(st, d)
	st.denyReason = "denied by interceptor: " + dn.message()
	toClient := append(wsframe.Encode(wsframe.OpText, dn.wsMessage(), false), wsframe.CloseFrame(code, dn.message(), false)...)
	return []*extprocv3.ProcessingResponse{
		bodyResponse(false, toClient, true),
		bodyResponse(true, wsframe.CloseFrame(code, "", true), true),
	}
}

// deny refuses the call with the provider's own error. The call still gets
// its record (the status it was refused with, and why).
func (srv *Server) deny(st *streamState, d interceptor.Decision) []*extprocv3.ProcessingResponse {
	st.enf.denied = true
	dn := srv.denial(st, d)
	st.status = dn.status
	st.denyReason = "denied by interceptor: " + dn.message()
	srv.emitComplete(st)
	return []*extprocv3.ProcessingResponse{{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{ImmediateResponse: &extprocv3.ImmediateResponse{
			Status: &typev3.HttpStatus{Code: typev3.StatusCode(dn.status)},
			Headers: &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{{
				Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")},
			}}},
			Body:    dn.body(),
			Details: "aintercept_interceptor_deny",
		}},
	}}
}

func (srv *Server) denial(st *streamState, d interceptor.Decision) denial {
	status := d.HTTPStatus
	if status == 0 {
		status = 403
	}
	return denial{status: status, reason: d.Reason, host: st.host, path: st.path}
}

// undecodable notes content enforce had to release unevaluated.
func (srv *Server) undecodable(st *streamState, what string) {
	if n := srv.undecodableCount.Add(1); n == 1 || n%100 == 0 {
		slog.Warn("enforce: content passed without evaluation", "what", what, "host", st.host, "total", n)
	}
}

func headersResponse(request bool, remove []string) *extprocv3.ProcessingResponse {
	common := &extprocv3.CommonResponse{}
	if len(remove) > 0 {
		common.HeaderMutation = &extprocv3.HeaderMutation{RemoveHeaders: remove}
	}
	hr := &extprocv3.HeadersResponse{Response: common}
	if request {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{RequestHeaders: hr}}
	}
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{ResponseHeaders: hr}}
}

func bodyResponse(request bool, body []byte, eos bool) *extprocv3.ProcessingResponse {
	br := &extprocv3.BodyResponse{Response: &extprocv3.CommonResponse{
		BodyMutation: &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_StreamedResponse{
			StreamedResponse: &extprocv3.StreamedBodyResponse{Body: body, EndOfStream: eos},
		}},
	}}
	if request {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{RequestBody: br}}
	}
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{ResponseBody: br}}
}
