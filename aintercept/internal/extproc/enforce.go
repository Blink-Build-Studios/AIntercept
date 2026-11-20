package extproc

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"slices"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// Enforce mode: streams from the enforcing ext_proc filter, in Envoy's
// FULL_DUPLEX_STREAMED body mode. Envoy forwards only what this server sends
// back, so every byte goes through here:
//
//   - Request headers are held until the whole request body has arrived, then
//     OnCallStart decides. With Interceptor content, the request's new
//     messages go to OnRequest (a Replace is put back in place in the body, or
//     in a Codex WebSocket's response.create). Requests go upstream unchanged
//     unless an answer replaced them; a refused one gets the provider's own
//     error.
//   - Responses: without content, chunks pass straight through. With content,
//     a reply's text is held piece by piece for OnReply and its tool calls
//     whole for OnToolCall, asked for uncompressed so answers never need
//     re-compressing. A Respond is delivered as a reply or by rewriting the
//     request (respond.go).
//
// Envoy's messages are read on their own goroutine, so a reply's pieces keep
// arriving while OnReply decides: the pending text is asked about only when
// nothing more is waiting, and pieces that came meanwhile go in the next
// call.
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
	respPass       respMode = iota // forward chunks as they arrive
	respToolsSSE                   // hold the event stream's text and tool calls
	respToolsWhole                 // decide the whole body's text and tool calls
	respSniff                      // a reply without a content-type, held until its first bytes say what it is
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
	// The reply's text and tool calls held (tools; a WebSocket's current
	// turn in wsTools), ended once a Respond or a refusal ended the reply;
	// toolDeny is the refusal.
	tools, wsTools *convo.Stream
	ended          bool
	toolDeny       *interceptor.Decision
	// more reports that more of the stream is waiting to be handled (nil:
	// nothing is).
	more func() bool
}

// waiting reports whether more of the stream is waiting to be handled.
func (e *enforceState) waiting() bool { return e.more != nil && e.more() }

// processEnforce serves an enforced stream, starting with its first request.
func (srv *Server) processEnforce(stream extprocv3.ExternalProcessor_ProcessServer, st *streamState, req *extprocv3.ProcessingRequest) error {
	st.enforce = true
	type recv struct {
		req *extprocv3.ProcessingRequest
		err error
	}
	in := make(chan recv, 64)
	st.enf = &enforceState{more: func() bool { return len(in) > 0 }}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			r, err := stream.Recv()
			select {
			case in <- recv{r, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		for _, r := range srv.enforceHandle(st, req) {
			if err := stream.Send(r); err != nil {
				srv.wsClose(st)
				return err
			}
		}
		r := <-in
		req = r.req
		err := r.err
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
		case e.denied, e.ended:
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
		if (e.respHeld || e.mode == respToolsSSE) && !e.denied && !e.ended { // held events end with the trailers
			out = srv.enforceResponseBody(st, nil, true)
		}
		srv.emitComplete(st)
		return append(out, continueResponseTrailers())
	}
	return nil
}

// enforceStart decides the call (OnCallStart, then OnRequest with content)
// and releases or refuses the request. hasBody: the request had a
// body (held in body; nil when it was too large to hold).
func (srv *Server) enforceStart(st *streamState, body []byte, hasBody bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	if hasBody && e.reqTooBig {
		return srv.deny(st, interceptor.Decision{Action: interceptor.Deny, Reason: "Request too large to evaluate.", HTTPStatus: 413})
	}
	ci := srv.callInfo(st, body)
	start := srv.decideStart(st, ci)
	if start.Action == interceptor.Deny || start.Action == interceptor.Refuse {
		return srv.deny(st, start)
	}
	requests := srv.enforcingRequests(st)
	var remove []string
	if (requests || srv.enforcingTurns(st)) && st.wsRequested {
		remove = append(remove, "sec-websocket-extensions") // client messages are re-framed
	}
	if start.Action == interceptor.Respond && (!hasBody || len(body) == 0) {
		srv.ic.Responded(st.correlationID, start, "") // nothing to answer (a WebSocket's requests come later)
	}
	if !hasBody {
		return []*extprocv3.ProcessingResponse{headersResponse(true, remove)}
	}
	fwd := body
	if start.Action == interceptor.Respond && len(body) > 0 {
		r, ok := srv.readRequest(st, body)
		out, rewritten := srv.respondHTTP(st, ci, r, ok, start)
		if out != nil {
			return out
		}
		if rewritten != nil {
			remove = append(remove, "content-length", "content-encoding")
		}
		return []*extprocv3.ProcessingResponse{headersResponse(true, remove), bodyResponse(true, firstOf(rewritten, body), true)}
	}
	if requests && len(body) > 0 {
		plain, err := events.DecodeBody(body, st.reqHeaders["content-encoding"])
		if err != nil {
			srv.ic.Unparsed("undecodable body")
		} else if r, ok := srv.parseRequest(st, plain); ok {
			switch d := srv.decideRequest(st, r); d.Action {
			case interceptor.Replace:
				fwd = d.Payload
				remove = append(remove, "content-length", "content-encoding")
			case interceptor.Deny, interceptor.Refuse:
				return srv.deny(st, d)
			case interceptor.Respond:
				out, rewritten := srv.respondHTTP(st, ci, r, true, d)
				if out != nil {
					return out
				}
				if rewritten != nil {
					fwd = rewritten
					remove = append(remove, "content-length", "content-encoding")
				}
			}
			if srv.holdsReply(r.Format) {
				st.icState().replyFormat = r.Format
				remove = append(remove, "accept-encoding") // replies the observer can read
			}
		}
	}
	return []*extprocv3.ProcessingResponse{headersResponse(true, remove), bodyResponse(true, fwd, true)}
}

func (srv *Server) enforceResponseHeaders(st *streamState, h *extprocv3.HttpHeaders) []*extprocv3.ProcessingResponse {
	e := st.enf
	if st.ws == nil && !h.GetEndOfStream() && st.ic != nil && st.ic.replyFormat != "" && srv.holdsReplies() {
		return srv.holdReplyHeaders(st)
	}
	if st.ws != nil {
		if srv.enforcingRequests(st) || srv.enforcingTurns(st) {
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
	return []*extprocv3.ProcessingResponse{continueResponseHeaders()}
}

func (srv *Server) enforceResponseBody(st *streamState, chunk []byte, eos bool) []*extprocv3.ProcessingResponse {
	e := st.enf
	switch e.mode {
	case respToolsSSE:
		return srv.toolsSSE(st, chunk, eos)

	case respToolsWhole:
		return srv.toolsWhole(st, chunk, eos)

	case respSniff:
		return srv.sniffReply(st, chunk, eos)
	}
	return []*extprocv3.ProcessingResponse{bodyResponse(false, chunk, eos)}
}

// enforceWS forwards a WebSocket body chunk: as is when nothing decides its
// turns. Otherwise client messages are re-framed, and each request (a
// response.create) goes to OnTurnStart, then with content to OnRequest; with
// content, the server's go through OnReply and OnToolCall when the
// Interceptor has rules on them (respond.go), else they're decoded for
// recording and forwarded as they came.
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
	if !toServer && !srv.holdsReplies() { // recorded, never held or changed
		for _, m := range msgs {
			srv.wsMessage(st, dir, m)
		}
		return []*extprocv3.ProcessingResponse{bodyResponse(false, b.GetBody(), b.GetEndOfStream())}
	}
	if err != nil {
		return srv.wsDeny(st, toServer, interceptor.Decision{Reason: "Undecodable WebSocket frame."}, 1002)
	}
	if !toServer {
		return srv.wsToolCalls(st, msgs, b.GetEndOfStream())
	}
	var out, toClient []byte
	for _, m := range msgs {
		srv.wsMessage(st, dir, m) // recording: turns and the connection row
		if m.Opcode >= 0x8 {
			out = append(out, wsframe.Encode(m.Opcode, m.Payload, toServer)...)
			continue
		}
		if m.Truncated {
			return srv.wsDeny(st, toServer, interceptor.Decision{Reason: "WebSocket message too large to evaluate."}, 1009)
		}
		d := srv.decideWSRequest(st, m)
		switch d.Action {
		case interceptor.Respond:
			if r, ok := srv.ic.Parser().ParseWebSocket(st.host, st.path, m.Payload); ok {
				var reply []byte
				d, reply = srv.respondWS(st, r, m.Payload, d)
				toClient = append(toClient, reply...)
			} else {
				srv.ic.Responded(st.correlationID, d, "") // a request it can't read: recorded, not applied
				d = interceptor.Decision{Action: interceptor.Allow}
			}
		case interceptor.Refuse:
			toClient = append(toClient, srv.refuseTurn(st, d)...)
		}
		switch d.Action {
		case interceptor.Replace:
			out = append(out, wsframe.Encode(m.Opcode, d.Payload, toServer)...)
		case interceptor.Respond, interceptor.Refuse: // answered here: the request never goes upstream
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
	res := []*extprocv3.ProcessingResponse{bodyResponse(toServer, out, b.GetEndOfStream())}
	if len(toClient) > 0 {
		res = append(res, bodyResponse(false, toClient, false))
	}
	return res
}

// decideWSRequest decides a client message on a WebSocket: a request (a
// response.create) goes to OnTurnStart, then with content to OnRequest.
func (srv *Server) decideWSRequest(st *streamState, m wsframe.Message) interceptor.Decision {
	allow := interceptor.Decision{Action: interceptor.Allow}
	if m.Opcode != wsframe.OpText || messageType(m.Payload) != "response.create" {
		return allow
	}
	if srv.enforcingTurns(st) {
		t := interceptor.TurnStart(st.icState().call, st.host, st.path, st.ws.turnsTotal+1, m.Payload)
		if d := srv.ic.DecideTurn(context.Background(), t); d.Action != interceptor.Allow {
			return d
		}
	}
	if !srv.enforcingRequests(st) {
		return allow
	}
	r, ok := srv.parseWSRequest(st, m)
	if !ok {
		return allow
	}
	return srv.decideRequest(st, r)
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

// deny refuses the call with the provider's own error: a Deny, or an Error
// answer (Refuse). The call still gets its record (the status it was refused
// with, and why).
func (srv *Server) deny(st *streamState, d interceptor.Decision) []*extprocv3.ProcessingResponse {
	st.enf.denied = true
	dn := srv.denial(st, d)
	st.status = dn.status
	st.denyReason = denyReason(d, dn)
	srv.emitComplete(st)
	var set []*corev3.HeaderValueOption
	h := dn.headers()
	for _, k := range slices.Sorted(maps.Keys(h)) {
		set = append(set, &corev3.HeaderValueOption{Header: &corev3.HeaderValue{Key: k, RawValue: []byte(h[k])}})
	}
	details := "aintercept_interceptor_deny"
	if d.Action == interceptor.Refuse {
		details = "aintercept_interceptor_error"
	}
	return []*extprocv3.ProcessingResponse{{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{ImmediateResponse: &extprocv3.ImmediateResponse{
			Status:  &typev3.HttpStatus{Code: typev3.StatusCode(dn.status)},
			Headers: &extprocv3.HeaderMutation{SetHeaders: set},
			Body:    dn.body(),
			Details: details,
		}},
	}}
}

// denyReason is the call's record of why it was refused.
func denyReason(d interceptor.Decision, dn denial) string {
	if d.Action == interceptor.Refuse {
		return "refused by interceptor: " + dn.message()
	}
	return "denied by interceptor: " + dn.message()
}

// denial is how the client is refused. An Error answer (Refuse) is written
// as the Interceptor gave it; AIntercept's own refusal (the Interceptor
// failed, or the call can't be evaluated) as a policy block.
func (srv *Server) denial(st *streamState, d interceptor.Decision) denial {
	status := d.HTTPStatus
	if status == 0 {
		status = 403
	}
	dn := denial{status: status, reason: d.Reason, errType: d.ErrorType, host: st.host, path: st.path}
	if d.Action != interceptor.Refuse {
		dn.code = "blocked_by_policy"
		switch dn.format() {
		case events.APIGemini:
			dn.errType = "PERMISSION_DENIED"
		case events.APIOpenAIResponses, events.APIOpenAIChat, events.APIChatGPTBackend:
			dn.errType = "invalid_request_error"
		default:
			dn.errType = "permission_error"
		}
	}
	return dn
}

// refuseTurn refuses one WebSocket turn (an Error answer): the client gets
// the API's error event, which ends the turn, and the request never goes
// upstream. The connection stays open.
func (srv *Server) refuseTurn(st *streamState, d interceptor.Decision) []byte {
	msg := srv.denial(st, d).wsMessage()
	srv.wsMessage(st, events.DirOutbound, wsframe.Message{Opcode: wsframe.OpText, Payload: msg}) // records the turn's end
	out := wsframe.Encode(wsframe.OpText, msg, false)
	st.ws.conn.BytesDown += len(out)
	return out
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
