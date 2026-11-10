package extproc

import (
	"encoding/json"
	"maps"
	"regexp"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// wsMessageCap bounds one decoded WebSocket message (a Codex response.create
// carries the turn's input, like an HTTP request body).
const wsMessageCap = reqBufferCap

// maxWSMessageTypes bounds the per-connection message-type tally.
const maxWSMessageTypes = 64

// wsState tracks one upgraded WebSocket connection. Bytes in both directions
// go through a decoder; complete messages are assembled into model turns
// (client response.create … server response.completed), each emitted as a
// complete row that the normal extractors parse, as for an HTTP model call.
type wsState struct {
	up, down   *wsframe.Decoder // client→server, server→client
	ext        wsframe.Extensions
	started    time.Time
	conn       events.WSConnection
	turn       *wsTurn
	turnsTotal int
}

// wsTurn is one model turn in progress.
type wsTurn struct {
	req       []byte // the client's response.create message
	resp      []byte // server events, SSE-framed ("data: {...}\n\n") for the extractors
	respTrunc bool
}

func newWSState(respHeaders map[string]string) *wsState {
	ext := wsframe.ParseExtensions(respHeaders["sec-websocket-extensions"])
	return &wsState{
		up:      wsframe.NewDecoder(ext.Deflate, ext.ClientNoContextTakeover, wsMessageCap),
		down:    wsframe.NewDecoder(ext.Deflate, ext.ServerNoContextTakeover, wsMessageCap),
		ext:     ext,
		started: time.Now(),
		conn:    events.WSConnection{Deflate: ext.Deflate, MessageTypes: map[string]int{}},
	}
}

// wsFeed decodes a body chunk of an upgraded connection.
func (srv *Server) wsFeed(st *streamState, dir events.Direction, chunk []byte) {
	ws := st.ws
	if ws.conn.Closed {
		// The connection row has been emitted; bytes after it (a client's
		// close reply, a late ping) aren't recorded.
		return
	}
	dec := ws.down
	if dir == events.DirInbound {
		dec = ws.up
		ws.conn.BytesUp += len(chunk)
	} else {
		ws.conn.BytesDown += len(chunk)
	}
	if dec.Broken() || len(chunk) == 0 {
		return
	}
	msgs, err := dec.Feed(chunk)
	if err != nil {
		ws.conn.Undecodable = true
	}
	for _, m := range msgs {
		srv.wsMessage(st, dir, m)
	}
}

// wsMessage folds one decoded message into the connection and current turn.
func (srv *Server) wsMessage(st *streamState, dir events.Direction, m wsframe.Message) {
	ws := st.ws
	if dir == events.DirInbound {
		ws.conn.MessagesUp++
	} else {
		ws.conn.MessagesDown++
	}
	if m.Opcode == wsframe.OpClose {
		ws.conn.CloseCode = m.CloseCode
		return
	}
	if m.Opcode != wsframe.OpText && m.Opcode != wsframe.OpBinary {
		return // ping / pong
	}
	if m.Truncated {
		ws.conn.TruncatedMessages++
	}
	typ := messageType(m.Payload)
	key := string(dir) + ":" + typ
	if _, ok := ws.conn.MessageTypes[key]; ok || len(ws.conn.MessageTypes) < maxWSMessageTypes {
		ws.conn.MessageTypes[key]++
	}

	switch {
	case dir == events.DirInbound && typ == "response.create":
		// A new turn. One at a time per connection; an unfinished one is
		// emitted as-is (it may lack usage).
		srv.wsEmitTurn(st)
		ws.turn = &wsTurn{req: m.Payload}
	case dir == events.DirOutbound && ws.turn != nil:
		// Streaming text/argument fragments carry content and nothing the
		// extractors read; the completed items and response carry the rest.
		if !strings.HasSuffix(typ, ".delta") {
			frame := append(append([]byte("data: "), m.Payload...), '\n', '\n')
			if finalEvents[typ] && len(ws.turn.resp)+len(frame) > respBufferCap && len(frame) <= finalEventCap {
				// The turn's usage and outcome: kept past the cap, on a line of its
				// own (the buffer may end mid-frame where the cap cut it).
				ws.turn.resp = append(append(ws.turn.resp, '\n', '\n'), frame...)
				ws.turn.respTrunc = true
			} else {
				ws.turn.resp, ws.turn.respTrunc = appendCapped(ws.turn.resp, frame, ws.turn.respTrunc, respBufferCap)
			}
		}
		switch typ {
		case "response.completed", "response.failed", "response.incomplete", "error":
			srv.wsEmitTurn(st)
		}
	}
}

// wsEmitTurn emits the current turn as a complete row (transport websocket),
// parsed downstream by the same extractors as an HTTP model call.
func (srv *Server) wsEmitTurn(st *streamState) {
	ws := st.ws
	t := ws.turn
	if t == nil {
		return
	}
	ws.turn = nil
	ws.turnsTotal++
	e := events.Event{
		Phase:         events.PhaseComplete,
		Transport:     events.TransportWebSocket,
		WSTurn:        ws.turnsTotal,
		Host:          st.host,
		Method:        st.method,
		Path:          st.path,
		Status:        st.status,
		KeyHash:       st.keyHash,
		KeyHint:       st.keyHint,
		Auth:          st.auth,
		IP:            st.ip,
		ProviderOrgID: events.ProviderOrgID(st.respHeaders),
		Synthetic:     st.synthetic,
		EndOfStream:   true,
	}
	e.AttachBodies(t.req, t.resp, "", st.respHeaders)
	srv.handOff(st, &e, len(t.req)+len(t.resp))
	// Per-turn identity is in the response.create body (client_metadata);
	// the upgrade request's copy is from when the connection opened.
	hdrs := make(map[string]string, len(st.reqHeaders))
	for k, v := range st.reqHeaders {
		if !strings.EqualFold(k, "x-codex-turn-metadata") {
			hdrs[k] = v
		}
	}
	e.AttachRequestHeaders(hdrs)
	srv.offer(st, e)
}

// wsClose finishes the connection: any unfinished turn, then one
// ws_connection row summarizing it.
func (srv *Server) wsClose(st *streamState) {
	ws := st.ws
	if ws == nil || ws.conn.Closed {
		return
	}
	srv.wsEmitTurn(st)
	ws.conn.Closed = true
	ws.conn.Turns = ws.turnsTotal
	ws.conn.DurationMS = time.Since(ws.started).Milliseconds()
	conn := ws.conn
	// The row is serialized on another goroutine; give it its own map.
	conn.MessageTypes = maps.Clone(ws.conn.MessageTypes)
	e := events.Event{
		Phase:         events.PhaseWSConnection,
		Transport:     events.TransportWebSocket,
		Host:          st.host,
		Method:        st.method,
		Path:          st.path,
		Status:        st.status,
		KeyHash:       st.keyHash,
		KeyHint:       st.keyHint,
		Auth:          st.auth,
		IP:            st.ip,
		ProviderOrgID: events.ProviderOrgID(st.respHeaders),
		Synthetic:     st.synthetic,
		WebSocket:     &conn,
	}
	// Run the header-based extraction (client/provider maps, account,
	// device/session from the upgrade's turn metadata); no bodies.
	e.AttachBodies(nil, nil, "", st.respHeaders)
	e.AttachRequestHeaders(st.reqHeaders)
	srv.offer(st, e)
}

// leadingType matches a JSON object whose first member is "type", as in
// every OpenAI Realtime/Responses WebSocket event.
var leadingType = regexp.MustCompile(`^\s*\{\s*"type"\s*:\s*"([^"\\]{1,128})"`)

// smallMessage is the size below which a message's type is read by fully
// parsing it (any key order); larger messages use the leading-key match.
const smallMessage = 64 << 10

// messageType returns a message's JSON "type" without parsing a large
// payload on the stream goroutine: "<non-json>" when there is none.
func messageType(p []byte) string {
	if m := leadingType.FindSubmatch(p); m != nil {
		return string(m[1])
	}
	if len(p) <= smallMessage {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(p, &head) == nil && head.Type != "" {
			return head.Type
		}
	}
	return "<non-json>"
}
