package drawer

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/gob"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Wire is one captured transaction as the drawer reads it (request_detail.Wire):
// what its detail object (calls/<id>.ndjson.gz) holds, or, from its request
// row, what that keeps. A wire read from the detail object is kept as the
// key's external input (state.SetExternal): the drawer's derivation reads it
// on every render, and it never changes once the call has ended. Bodies aren't
// kept: what the drawer shows of them (its content rows, the stream's event
// count, the tools' inputs) is.
type Wire struct {
	CID         string
	TS          int64
	Method      Value
	Path        string
	ReqHeaders  Value // a dict
	IP          Value
	KeyHash     Value
	Status      Value
	RespTS      Value
	RespHeaders Value // a dict, or None
	Complete    Value // a dict, or None
	EndTS       Value
	Up          Value
	Down        Value
	DownFirst   Value
	DownLast    Value
	// FromTables: built from the request row (its detail object couldn't be
	// read, or it's a WebSocket turn).
	FromTables bool

	// What the drawer shows of retained bodies.
	Retained         bool
	SentRetained     []ContentRow // nil: _sent_retained is None
	HasSentRetained  bool
	ReceivedRetained []ContentRow
	SSECount         int
	// ToolInputs are the response's tool_use blocks' inputs, by block id
	// (request_detail._tool_details), each as its input rows (nil: not a dict).
	ToolInputs []ToolInput
}

// ToolInput is one tool_use block's id and input rows.
type ToolInput struct {
	ID     Value
	IsDict bool
	Rows   []DetailRow
}

// Missing is the external input of a call whose detail object wasn't there
// (or held none of its rows): rendered from its request row, as today's drawer
// does for a missing object.
type Missing struct{ At int64 }

func init() {
	gob.Register(&Wire{})
	gob.Register(Missing{})
	gob.Register(BatchParts{})
}

// C is w.c(key): the complete row's value.
func (w *Wire) C(key string) Value { return w.Complete.Get(key) }

// Kind is pp.kind_of(path).
func (w *Wire) Kind() string { return KindOf(w.Path) }

func (w *Wire) prompt() Value          { return w.ReqHeaders.Get("x-claude-code-prompt-id") }
func (w *Wire) requestClass() Value    { return w.ReqHeaders.Get("x-claude-code-request-class") }
func (w *Wire) clientRequestID() Value { return w.ReqHeaders.Get("x-client-request-id") }
func (w *Wire) requestID() Value       { return w.RespHeaders.Get("request-id") }
func (w *Wire) errorV() Value          { return w.Complete.Get("error") }

// failed is w.failed.
func (w *Wire) failed() bool {
	if w.Status.IsNone() {
		return !w.EndTS.IsNone()
	}
	n, ok := w.Status.float()
	return ok && n >= 400
}

// KindOf is person.kind_of: a path's channel.
func KindOf(path string) string {
	p, _, _ := strings.Cut(path, "?")
	switch {
	case p == "/backend-api/codex/responses":
		return "model"
	case strings.HasPrefix(p, "/backend-api/codex/analytics-events"):
		return "analytics"
	case strings.HasPrefix(p, "/otlp/"):
		return "metrics"
	case strings.HasPrefix(p, "/backend-api/wham/"), strings.HasPrefix(p, "/backend-api/accounts/"):
		return "usage"
	case strings.HasPrefix(p, "/backend-api/ps/"), strings.HasPrefix(p, "/backend-api/plugins/"),
		strings.HasPrefix(p, "/backend-api/codex/models"):
		return "catalog"
	case strings.HasPrefix(p, "/v1/messages/count_tokens"):
		return "count"
	case strings.HasPrefix(p, "/v1/messages"):
		return "model"
	case strings.HasPrefix(p, "/api/event_logging"):
		return "analytics"
	case strings.HasPrefix(p, "/api/eval/"):
		return "flags"
	}
	return "account"
}

// Lines is raw.lines: a detail object's non-empty lines (none when it's
// missing or truncated).
func Lines(gz []byte) [][]byte {
	if gz == nil {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, ln := range bytes.Split(data, []byte("\n")) {
		if ln = bytes.Trim(ln, " \t\n\r\v\f"); len(ln) > 0 {
			out = append(out, ln)
		}
	}
	return out
}

// rowsOf is store.rows_of: the lines' rows that are the transaction's.
func rowsOf(lines [][]byte, cid string) []Value {
	var out []Value
	for _, ln := range lines {
		if !bytes.Contains(ln, []byte(cid)) {
			continue
		}
		r, ok := Loads(ln)
		if !ok || r.K != Object {
			continue
		}
		if c := r.Get("correlation_id"); c.K == Str && c.S == cid {
			out = append(out, r)
		}
	}
	return out
}

func orZero(v Value) float64 {
	if !v.Truthy() {
		return 0
	}
	f, _ := v.float()
	return f
}

func dictOf(v Value) Value {
	if v.K == Object {
		return v
	}
	return Value{K: Object}
}

// ParseDetail is request_detail._load_wire over a call's detail object: nil
// when it holds no request headers row (the drawer then builds the call from
// its request row).
func ParseDetail(cid string, gz []byte) *Wire {
	rows := rowsOf(Lines(gz), cid)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := orZero(rows[i].Get("seq")), orZero(rows[j].Get("seq"))
		if a != b {
			return a < b
		}
		return orZero(rows[i].Get("timestamp_ns")) < orZero(rows[j].Get("timestamp_ns"))
	})
	var w *Wire
	disconnect := None
	for _, r := range rows {
		phase, ts, status := r.Get("phase"), r.Get("timestamp_ns"), r.Get("status")
		ph := phase.S
		if phase.K != Str {
			ph = ""
		}
		switch {
		case ph == "request_headers" && w == nil && ts.IsInt():
			n, _ := ts.Int64()
			path := r.Get("path").Or(StrV(""))
			w = &Wire{CID: cid, TS: n, Method: r.Get("method"), Path: path.Str(), ReqHeaders: dictOf(r.Get("headers").Or(Value{K: Object})),
				IP: r.Get("ip"), KeyHash: r.Get("key_hash"), RespHeaders: None, Complete: None}
		case w != nil && ph == "response_headers" && w.RespTS.IsNone():
			w.Status, w.RespTS, w.RespHeaders = status, ts, dictOf(r.Get("headers").Or(Value{K: Object}))
		case w != nil && ph == "complete" && w.Complete.IsNone() && !r.Get("transport").Eq(StrV("websocket")):
			w.Complete, w.EndTS = r, ts
			if w.Status.IsNone() && !status.IsNone() && r.Get("error").IsNone() {
				w.Status = status
			}
		case w != nil && ph == "disconnect" && disconnect.IsNone():
			disconnect = r
		}
	}
	if w == nil {
		return nil
	}
	if w.Complete.IsNone() && !disconnect.IsNone() {
		w.Complete, w.EndTS = disconnect, disconnect.Get("timestamp_ns")
	}
	var reqChunks, respChunks []chunk
	for _, phase := range []string{"request_body_chunk", "response_body_chunk"} {
		var chunks []Value
		for _, r := range rows {
			if p := r.Get("phase"); p.K == Str && p.S == phase {
				chunks = append(chunks, r)
			}
		}
		if len(chunks) == 0 {
			continue
		}
		var total int64
		var times []int64
		for _, r := range chunks {
			if n := pyInt(r.Get("bytes").Or(IntV(0))); n != nil {
				total += *n
			}
			if t := r.Get("timestamp_ns"); t.Truthy() {
				if n, ok := t.Int64(); ok {
					times = append(times, n)
				}
			}
		}
		lo, hi := None, None
		for i, t := range times {
			if i == 0 || t < mustInt(lo) {
				lo = IntV(t)
			}
			if i == 0 || t > mustInt(hi) {
				hi = IntV(t)
			}
		}
		if phase == "request_body_chunk" {
			w.Up = IntV(total)
		} else {
			w.Down, w.DownFirst, w.DownLast = IntV(total), lo, hi
		}
		for _, r := range chunks {
			b := r.Get("body_b64")
			if b.IsNone() || b.K != Str {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(b.S)
			if err != nil {
				continue
			}
			t, _ := r.Get("timestamp_ns").Or(IntV(0)).Int64()
			if phase == "request_body_chunk" {
				reqChunks = append(reqChunks, chunk{t, raw})
			} else {
				respChunks = append(respChunks, chunk{t, raw})
			}
		}
	}
	w.Retained = len(reqChunks) > 0 || len(respChunks) > 0
	if w.Retained {
		w.retainedViews(reqChunks, respChunks)
	}
	// The wire is kept with the key's state: of the headers, only what the
	// render reads (the bodies' views above are done with them).
	w.ReqHeaders = keepKeys(w.ReqHeaders, func(k string) bool { return readReqHeaders[k] })
	w.RespHeaders = keepKeys(w.RespHeaders, readRespHeader)
	return w
}

// readReqHeaders are the request headers the render reads (Wire.prompt,
// requestClass, clientRequestID, scope.sessionOf), by exact name.
var readReqHeaders = map[string]bool{"x-claude-code-prompt-id": true, "x-claude-code-request-class": true,
	"x-client-request-id": true, "x-claude-code-session-id": true}

// readRespHeader is whether the render reads a response header: request-id
// (Wire.requestID), the unified rate limits (limit: by lowered name, the first
// that matches) and Codex's (codexLimits: by exact name).
func readRespHeader(k string) bool {
	return k == "request-id" || strings.HasPrefix(k, "x-codex-") ||
		strings.HasPrefix(strings.ToLower(k), "anthropic-ratelimit-unified-")
}

// keepKeys is a dict's members whose key keep accepts, in order; any other
// value as it is.
func keepKeys(v Value, keep func(string) bool) Value {
	if v.K != Object {
		return v
	}
	out := Value{K: Object, O: []Member{}}
	for _, m := range v.O {
		if keep(m.Key) {
			out.O = append(out.O, m)
		}
	}
	return out
}

func mustInt(v Value) int64 {
	n, _ := v.Int64()
	return n
}

type chunk struct {
	ts   int64
	data []byte
}

// --- retained bodies --------------------------------------------------------------

// header is request_detail._header: the first header whose name, lowered, is name.
func header(h Value, name string) Value {
	if h.K != Object {
		return None
	}
	for _, m := range h.O {
		if strings.ToLower(m.Key) == name {
			return m.Val
		}
	}
	return None
}

// decodeBody is request_detail._decode: body bytes as text per content-encoding;
// ok false when it can't be decoded here.
func decodeBody(raw []byte, encoding Value) (string, bool) {
	enc := "identity"
	if encoding.Truthy() {
		enc = encoding.Str()
	}
	enc = strip(strings.ToLower(enc))
	var data []byte
	switch enc {
	case "identity", "":
		data = raw
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", false
		}
		if data, err = io.ReadAll(zr); err != nil {
			return "", false
		}
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", false
		}
		if data, err = io.ReadAll(zr); err != nil {
			return "", false
		}
	default:
		return "", false
	}
	return decodeReplace(data), true
}

func joined(cs []chunk) []byte {
	var b []byte
	for _, c := range cs {
		b = append(b, c.data...)
	}
	return b
}

func bodyText(cs []chunk, h Value) (string, bool) {
	if len(cs) == 0 {
		return "", false
	}
	text, ok := decodeBody(joined(cs), header(h, "content-encoding"))
	if !ok {
		return "", false
	}
	return redact(text), true
}

func isStream(w *Wire) bool {
	ct := header(w.RespHeaders, "content-type")
	return strings.Contains(ct.Or(StrV("")).Str(), "event-stream")
}

type sse struct {
	typ  string
	data Value
}

var blockSplit = regexp.MustCompile(`\r?\n\r?\n`)

// sseEvents is request_detail.sse_events (their times aren't kept: the drawer's
// summary reads only their count and contents).
func sseEvents(w *Wire, resp []chunk) []sse {
	if len(resp) == 0 || !isStream(w) {
		return nil
	}
	enc := "identity"
	if e := header(w.RespHeaders, "content-encoding"); e.Truthy() {
		enc = e.Str()
	}
	enc = strings.ToLower(enc)
	var text string
	if enc == "identity" || enc == "" {
		var b strings.Builder
		for _, c := range resp {
			b.WriteString(decodeReplace(c.data))
		}
		text = b.String()
	} else {
		text, _ = bodyText(resp, w.RespHeaders)
	}
	var out []sse
	for _, block := range blockSplit.Split(text, -1) {
		name := ""
		hasName := false
		var data []string
		for _, line := range splitLines(block) {
			if strings.HasPrefix(line, "event:") {
				name, hasName = strip(line[6:]), true
			} else if strings.HasPrefix(line, "data:") {
				data = append(data, strip(line[5:]))
			}
		}
		if (!hasName || name == "") && len(data) == 0 {
			continue
		}
		payload, ok := Loads([]byte(strings.Join(data, "\n")))
		if !ok || payload.K != Object {
			payload = Value{K: Object}
		}
		typ := name
		if typ == "" {
			typ = payload.Get("type").Or(StrV("message")).Str()
		}
		out = append(out, sse{typ, scrub(payload)})
	}
	return out
}

// block is request_detail.Block.
type block struct {
	index int
	typ   string
	text  string
	name  Value
	id    Value
	input Value
	has   bool // input set (a stream's partial JSON, or a body's)
}

// responseBlocks is request_detail.response_blocks.
func responseBlocks(w *Wire, events []sse, resp []chunk) []block {
	if isStream(w) {
		blocks := map[int]*block{}
		partial := map[int]string{}
		var order []int
		for _, e := range events {
			d := e.data
			iv := d.Get("index")
			i, isInt := iv.Int64()
			isInt = isInt && iv.IsInt()
			switch {
			case e.typ == "content_block_start" && isInt:
				cb := d.Get("content_block").Or(Value{K: Object})
				if _, ok := blocks[int(i)]; !ok {
					order = append(order, int(i))
				}
				blocks[int(i)] = &block{index: int(i), typ: cb.Get("type").Or(StrV("?")).Str(), name: cb.Get("name"), id: cb.Get("id")}
			case e.typ == "content_block_delta" && isInt && blocks[int(i)] != nil:
				delta := d.Get("delta").Or(Value{K: Object})
				if delta.Has("partial_json") {
					partial[int(i)] += delta.Get("partial_json").Str()
				}
				blocks[int(i)].text += delta.Get("text").Or(delta.Get("thinking")).Or(StrV("")).Str()
			}
		}
		for i, j := range partial {
			b := blocks[i]
			b.has = true
			if j == "" {
				b.input = Value{K: Object}
			} else if v, ok := Loads([]byte(j)); ok {
				b.input = v
			} else {
				b.input = None
			}
		}
		sort.Ints(order)
		out := make([]block, 0, len(order))
		for _, i := range order {
			out = append(out, *blocks[i])
		}
		return out
	}
	text, ok := bodyText(resp, w.RespHeaders)
	if !ok {
		return nil
	}
	body, ok := Loads([]byte(text))
	if !ok || body.K != Object {
		return nil
	}
	content := body.Get("content")
	if content.K != List {
		return nil
	}
	var out []block
	for i, cb := range content.L {
		if cb.K != Object {
			continue
		}
		out = append(out, block{index: i, typ: cb.Get("type").Or(StrV("?")).Str(),
			text: cb.Get("text").Or(cb.Get("thinking")).Or(StrV("")).Str(), name: cb.Get("name"), id: cb.Get("id"),
			input: cb.Get("input"), has: true})
	}
	return out
}

// retainedViews keeps what the drawer shows of the retained bodies.
func (w *Wire) retainedViews(req, resp []chunk) {
	events := sseEvents(w, resp)
	w.SSECount = len(events)
	blocks := responseBlocks(w, events, resp)
	w.SentRetained, w.HasSentRetained = sentRetained(req, w.ReqHeaders)
	w.ReceivedRetained = receivedRetained(blocks)
	seen := map[string]int{}
	for _, b := range blocks {
		if b.typ != "tool_use" {
			continue
		}
		ti := ToolInput{ID: b.id, IsDict: b.input.K == Object}
		if ti.IsDict {
			for _, m := range b.input.O {
				ti.Rows = append(ti.Rows, inputRow(m.Key, m.Val))
			}
		}
		k := b.id.Repr()
		if i, ok := seen[k]; ok { // a dict by id: the last block wins
			w.ToolInputs[i] = ti
			continue
		}
		seen[k] = len(w.ToolInputs)
		w.ToolInputs = append(w.ToolInputs, ti)
	}
}

// toolInput is the tool_use block with this id (inputs.get(tool.toolUseId)).
func (w *Wire) toolInput(id Value) *ToolInput {
	if !w.Retained {
		return nil
	}
	k := id.Repr()
	for i := range w.ToolInputs {
		if w.ToolInputs[i].ID.Repr() == k {
			return &w.ToolInputs[i]
		}
	}
	return nil
}

const bodyPreviewChars = 4000

func linesOf(text string) string {
	n := 0
	if text != "" {
		n = strings.Count(text, "\n") + 1
	}
	if n == 1 {
		return "1 line"
	}
	return itoa(int64(n)) + " lines"
}

func preview(text string) string {
	if runes(text) <= bodyPreviewChars {
		return text
	}
	head, rest := prefixRunes(text, bodyPreviewChars)
	return head + "\n… " + itoa(int64(strings.Count(rest, "\n"))) + " more lines"
}

func blockText(content Value) string {
	switch content.K {
	case Str:
		return content.S
	case List:
		var parts []string
		for _, c := range content.L {
			if c.K == Object {
				parts = append(parts, c.Get("text").Or(StrV("")).Str())
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// sentRetained is request_detail._sent_retained: the messages new in this
// request; ok false where it's None.
func sentRetained(req []chunk, h Value) ([]ContentRow, bool) {
	text, ok := bodyText(req, h)
	if !ok {
		return nil, false
	}
	body, ok := Loads([]byte(text))
	if !ok || body.K != Object {
		return nil, false
	}
	msgs := body.Get("messages")
	if msgs.K != List {
		return nil, false
	}
	names := map[string]string{}
	last := -1
	for i, m := range msgs.L {
		if m.K == Object && m.Get("role").Eq(StrV("assistant")) {
			last = i
			content := m.Get("content")
			if content.K == List {
				for _, cb := range content.L {
					if cb.K == Object && cb.Get("type").Eq(StrV("tool_use")) && cb.Get("id").Truthy() {
						names[cb.Get("id").Str()] = cb.Get("name").Or(StrV("")).Str()
					}
				}
			}
		}
	}
	out := []ContentRow{}
	for _, m := range msgs.L[last+1:] {
		content := None
		if m.K == Object {
			content = m.Get("content")
		}
		var blocks Value
		if content.K == Str {
			blocks = Value{K: List, L: []Value{{K: Object, O: []Member{{"type", StrV("text")}, {"text", content}}}}}
		} else {
			blocks = content.Or(Value{K: List})
		}
		if blocks.K != List {
			continue
		}
		for _, cb := range blocks.L {
			if cb.K != Object {
				continue
			}
			kind := cb.Get("type").Or(StrV("?")).Str()
			switch kind {
			case "tool_result":
				text := blockText(cb.Get("content"))
				tool := names[cb.Get("tool_use_id").Or(StrV("")).Str()]
				label := "tool_result"
				if tool != "" {
					label = "tool_result · " + tool
				}
				detail := linesOf(text)
				if cb.Get("is_error").Truthy() {
					detail += " · error"
				}
				out = append(out, ContentRow{Label: label, Detail: detail, Size: i64(int64(len(text))), Unit: sp("bytes"), Body: sp(preview(text))})
			case "text":
				text := cb.Get("text").Or(StrV("")).Str()
				n := int64(runes(text))
				out = append(out, ContentRow{Label: "text", Detail: itoa(n) + " chars", Size: &n, Unit: sp("chars"), Body: sp(preview(text))})
			default:
				out = append(out, ContentRow{Label: kind, Detail: ""})
			}
		}
	}
	return out, true
}

// receivedRetained is request_detail._received_retained.
func receivedRetained(blocks []block) []ContentRow {
	out := []ContentRow{}
	for _, b := range blocks {
		if b.typ == "tool_use" {
			text := ""
			if b.has && !b.input.IsNone() {
				text = Dumps(scrub(b.input), ", ", ": ", 2)
			}
			name := "?"
			if b.name.Truthy() {
				name = b.name.Str()
			}
			detail := ""
			if b.id.Truthy() {
				detail = b.id.Str()
			}
			out = append(out, ContentRow{Label: "tool_use · " + name, Detail: detail, Size: i64(int64(len(text))), Unit: sp("bytes"),
				Tone: sp("tool"), Body: sp(preview(text))})
			continue
		}
		n := int64(runes(b.text))
		out = append(out, ContentRow{Label: b.typ, Detail: itoa(n) + " chars", Size: &n, Unit: sp("chars"), Body: sp(preview(b.text))})
	}
	return out
}

// inputRow is request_detail._input_row.
func inputRow(k string, v Value) DetailRow {
	text := v.S
	if v.K != Str {
		text = Dumps(scrub(v), ", ", ": ", -1)
	}
	text = redact(text)
	if runes(text) <= 120 && !strings.Contains(text, "\n") {
		return row(k, StrV(text), "")
	}
	r := row(k, StrV(linesOf(text)+" · "+itoa(int64(runes(text)))+" chars"), "")
	r.Tone = sp("muted")
	return r
}
