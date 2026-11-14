package drawer

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// The derivation: request_detail.detail, details and reported_detail, ported
// line by line over the scope (scope.go). Values a Python expression passes
// through (a header's, the complete row's) stay Values, so the API's model
// validates the same thing today's code hands it.

const ns = 1_000_000_000

// facts is request_detail.Facts.
type facts struct {
	success *ev
	query   *ev
	stall   *ev
	tools   []*ev
	retry   *ev
	joins   *joins
	events  []*ev
}

// joins is session._Joins, over the events about one call.
type joins struct {
	success map[string]*ev
	tools   map[string][]*ev
	bash    map[string]*ev
}

func joinsOf(evs []*ev) *joins {
	j := &joins{success: map[string]*ev{}, tools: map[string][]*ev{}, bash: map[string]*ev{}}
	for _, e := range evs {
		rid := e.mdStr("requestId")
		switch {
		case e.name == "tengu_api_success":
			for _, k := range []*string{rid, e.mdStr("clientRequestId")} {
				if k != nil && *k != "" {
					if _, ok := j.success[*k]; !ok {
						j.success[*k] = e
					}
				}
			}
		case (e.name == "tengu_tool_use_success" || e.name == "tengu_tool_use_error") && rid != nil && *rid != "":
			j.tools[*rid] = append(j.tools[*rid], e)
		case e.name == "tengu_bash_tool_command_executed" && e.get("tool_use_id").Truthy():
			j.bash[e.get("tool_use_id").Str()] = e
		}
	}
	return j
}

func strOf(v Value) string {
	if v.K == Str {
		return v.S
	}
	return v.Str()
}

// factsOf is request_detail.facts.
func (s *scope) factsOf(sid Value, rid, crid Value, w *Wire) facts {
	if !sid.Truthy() {
		return facts{}
	}
	x := s.session(strOf(sid))
	prompt := None
	if w != nil {
		prompt = w.prompt()
	}
	var ids []string
	for _, v := range []Value{rid, crid} {
		if v.Truthy() {
			ids = append(ids, strOf(v))
		}
	}
	evs := x.about(prompt, ids, nil)
	in := func(v *string) bool {
		if v == nil {
			return false
		}
		for _, id := range ids {
			if id == *v {
				return true
			}
		}
		return false
	}
	var toolIDs []string
	for _, e := range evs {
		if in(e.mdStr("requestId")) {
			if t := e.mdStr("toolUseID"); t != nil && *t != "" {
				toolIDs = append(toolIDs, *t)
			}
		}
	}
	if len(toolIDs) > 0 {
		seen := map[string]bool{}
		for _, e := range evs {
			seen[e.id] = true
		}
		for _, e := range x.about(None, nil, toolIDs) {
			if !seen[e.id] {
				evs = append(evs, e)
			}
		}
		sort.SliceStable(evs, func(i, j int) bool {
			if evs[i].ts != evs[j].ts {
				return evs[i].ts < evs[j].ts
			}
			return evs[i].id < evs[j].id
		})
	}
	j := joinsOf(evs)
	f := facts{joins: j}
	if rid.Truthy() {
		f.success = j.success[strOf(rid)]
	}
	if f.success == nil && crid.Truthy() {
		f.success = j.success[strOf(crid)]
	}
	if rid.Truthy() {
		for _, e := range evs {
			if e.name == "tengu_streaming_stall_summary" && e.get("request_id").Eq(rid) {
				f.stall = e
				break
			}
		}
		f.tools = j.tools[strOf(rid)]
	}
	var ts int64
	p := None
	switch {
	case w != nil:
		ts, p = w.TS, w.prompt()
	case f.success != nil:
		ts, p = f.success.ts, f.success.prompt
	}
	for _, e := range evs {
		if e.name == "tengu_api_query" && e.prompt.Eq(p) && ts-60*ns <= e.ts && e.ts <= ts+2*ns {
			f.query = e
		}
	}
	if w != nil && w.failed() {
		var retries []*ev
		for _, e := range evs {
			if e.name == "tengu_api_retry" {
				retries = append(retries, e)
			}
		}
		f.retry = nearestRetry(retries, w.TS, w.EndTS, w.prompt())
	}
	for _, e := range []*ev{f.query, f.success, f.retry, f.stall} {
		if e != nil {
			f.events = append(f.events, e)
		}
	}
	f.events = append(f.events, f.tools...)
	sort.SliceStable(f.events, func(i, j int) bool {
		a, b := f.events[i], f.events[j]
		if a.ts != b.ts {
			return a.ts < b.ts
		}
		return a.id < b.id
	})
	return f
}

const retryAfterNS = 30 * ns

// nearestRetry is person.nearest_retry.
func nearestRetry(retries []*ev, ts int64, end Value, prompt Value) *ev {
	stop := ts
	if end.Truthy() {
		stop, _ = end.Int64()
	}
	var best *ev
	var bd, bat int64
	for _, r := range retries {
		at := r.ts
		if !(ts <= at && at <= stop+retryAfterNS) {
			continue
		}
		if !prompt.IsNone() && !(r.prompt.IsNone() || r.prompt.Eq(prompt)) {
			continue
		}
		d := at - stop
		if d < 0 {
			d = -d
		}
		if best == nil || d < bd || d == bd && at < bat {
			best, bd, bat = r, d, at
		}
	}
	return best
}

// --- shared pieces --------------------------------------------------------------

type tokens struct {
	input, cacheRead, cacheWrite, write5m, write1h, output *int64
	accuracy                                               string
}

func (t tokens) value() Value {
	return obj("input", vInt(t.input), "cacheRead", vInt(t.cacheRead), "cacheWrite", vInt(t.cacheWrite),
		"cacheWrite5m", vInt(t.write5m), "cacheWrite1h", vInt(t.write1h), "output", vInt(t.output), "accuracy", StrV(t.accuracy))
}

func tokensOf(w *Wire, md Value) tokens {
	if w == nil {
		out := pyInt(md.Get("outputTokens"))
		t := tokens{input: pyInt(md.Get("uncachedInputTokens")), cacheRead: pyInt(md.Get("cachedInputTokens")),
			write5m: pyInt(md.Get("cache_creation_5m_input_tokens")), write1h: pyInt(md.Get("cache_creation_1h_input_tokens")),
			output: out, accuracy: "missing"}
		if out != nil {
			t.accuracy = "estimated"
		}
		return t
	}
	out := pyInt(w.C("output_tokens"))
	t := tokens{input: pyInt(w.C("input_tokens")), cacheRead: pyInt(w.C("cache_read_tokens")),
		cacheWrite: pyInt(w.C("cache_write_tokens")), write5m: pyInt(md.Get("cache_creation_5m_input_tokens")),
		write1h: pyInt(md.Get("cache_creation_1h_input_tokens")), output: out, accuracy: "missing"}
	if out != nil {
		t.accuracy = "exact"
	}
	return t
}

func cachedPct(t tokens) *float64 {
	var total int64
	for _, p := range []*int64{t.input, t.cacheRead, t.cacheWrite} {
		if p != nil {
			total += *p
		}
	}
	if t.cacheRead == nil || total == 0 {
		return nil
	}
	return f64(round1(float64(*t.cacheRead) / float64(total) * 100))
}

func thinking(v Value) Value {
	if v.IsNone() || v.K == Str && v.S == "" {
		return None
	}
	return BoolV(v.Str() != "disabled")
}

func toolCounts(w *Wire) []string {
	out := []string{}
	if w == nil {
		return out
	}
	uses := w.C("tool_uses")
	if !uses.Truthy() {
		return out
	}
	var items []Value
	switch uses.K {
	case List:
		items = uses.L
	case Object:
		return out
	}
	for _, u := range items {
		if u.K != Object || !u.Get("name").Truthy() {
			continue
		}
		n := int64(1)
		if c := pyInt(u.Get("count")); c != nil && *c != 0 {
			n = *c
		}
		name := u.Get("name").Str()
		if n == 1 {
			out = append(out, name)
		} else {
			out = append(out, name+" ×"+itoa(n))
		}
	}
	return out
}

// position is request_detail.Position.
type position struct {
	promptIndex *int64
	stepIndex   *int64
	stepCount   *int64
	prev, next  Value
	prevMessage Value
	nextMessage Value
	calls       []*step
}

func (s *scope) positionOf(x *sess, w *Wire) position {
	if x == nil || !x.exists || w.Kind() != "model" {
		return position{}
	}
	steps := x.steps
	mid, prevMid := None, None
	if c, ok := x.chain[w.CID]; ok {
		mid, prevMid = c[0], c[1]
	}
	byMid, byPrev := x.byMid, x.byPrev
	var same []*step
	if wp := w.prompt(); wp.Truthy() {
		if wp.K == Str {
			same = x.byPrompt[wp.S]
		} else {
			for _, st := range steps {
				if st.prompt.Eq(wp) {
					same = append(same, st)
				}
			}
		}
	}
	var lane []*step
	for _, st := range same {
		if st.class.Eq(w.requestClass()) {
			lane = append(lane, st)
		}
	}
	at := -1
	for i, st := range lane {
		if st.id == w.CID {
			at = i
			break
		}
	}
	p := position{calls: steps, prevMessage: prevMid, prev: None, next: None, nextMessage: None}
	key := ""
	if prevMid.Truthy() {
		key = prevMid.S
	}
	if c, ok := byMid[key]; ok && c != "" {
		p.prev = StrV(c)
	} else if at > 0 {
		p.prev = StrV(lane[at-1].id)
	}
	if mid.Truthy() {
		if c, ok := byPrev[mid.S]; ok {
			p.next = StrV(c)
		}
	}
	if p.next.IsNone() && at >= 0 && at+1 < len(lane) {
		p.next = StrV(lane[at+1].id)
	}
	for i, st := range same {
		if st.id == w.CID {
			p.stepIndex = i64(int64(i + 1))
			break
		}
	}
	if len(same) > 0 {
		p.stepCount = i64(int64(len(same)))
	}
	if n, ok := x.index[w.prompt().Or(StrV("")).Str()]; ok {
		p.promptIndex = i64(n)
	}
	if p.next.Truthy() {
		if c, ok := x.chain[p.next.S]; ok {
			p.nextMessage = c[0]
		}
	}
	return p
}

// retryInfo is request_detail._retry_info.
func retryInfo(w *Wire, f facts, pos position) Value {
	if f.retry == nil {
		return None
	}
	attempt := int64(1)
	if a := pyInt(f.retry.get("attempt")); a != nil && *a != 0 {
		attempt = *a
	}
	var nxt *step
	for _, x := range pos.calls {
		xt, ok := x.ts.Int64()
		if ok && !x.ts.IsNone() && xt > w.TS && x.prompt.Eq(w.prompt()) && x.class.Eq(w.requestClass()) {
			nxt = x
			break
		}
	}
	status := None
	if nxt != nil {
		status = nxt.status
		if status.IsNone() && !nxt.end.IsNone() {
			status = StrV("ERR")
		}
	}
	done := false
	if nxt != nil && status.K == Int {
		n, _ := status.Int64()
		done = n < 400
	}
	of := None
	if done {
		of = IntV(attempt + 1)
	}
	next, at := None, None
	if nxt != nil {
		next = StrV(nxt.id)
		t, _ := nxt.ts.Int64()
		at = dt(t)
	}
	return obj("attempt", IntV(attempt), "of", of, "backoffMs", vFloat(pyFloat(f.retry.get("delayMs"))),
		"nextRequestId", next, "nextStatus", status, "nextAt", at)
}

func phrase(status Value) *string {
	n, ok := status.Int64()
	if !ok || status.K != Int {
		return nil
	}
	return process.StatusPhrase(n)
}

// notice is request_detail._notice.
func notice(w *Wire, retry Value) Value {
	if !w.failed() {
		return None
	}
	took := msBetween(IntV(w.TS), w.EndTS)
	var title, body string
	if w.Status.IsNone() {
		title = "✕ " + w.errorV().Or(StrV("Connection failed")).Str()
		body = "The request was sent but no response arrived"
		if took != nil {
			body += ": the connection failed after " + secs(*took) + "."
		} else {
			body += "."
		}
	} else {
		title = "✕ " + w.Status.Str()
		if ph := phrase(w.Status); ph != nil && *ph != "" {
			title += " " + *ph
		}
		body = "Anthropic answered " + w.Status.Str()
		if took != nil {
			body += " after " + secs(*took) + "."
		} else {
			body += "."
		}
	}
	if retry.K == Object {
		wait := ""
		if b := retry.Get("backoffMs"); !b.IsNone() {
			wait = " waited " + secs(b.F) + " and"
		}
		var outcome string
		switch {
		case !retry.Get("of").IsNone():
			outcome = "the retry succeeded."
		case !retry.Get("nextStatus").IsNone():
			outcome = "the retry failed too."
		default:
			outcome = "no retry was captured."
		}
		body += " Claude Code" + wait + " sent the request again (tengu_api_retry, attempt " + retry.Get("attempt").Str() + "); " + outcome
	}
	return obj("tone", StrV("neutral"), "title", StrV(title), "body", StrV(body))
}

func sentShape(w *Wire, md Value) []ContentRow {
	detail := "not retained"
	if n := pyInt(md.Get("messageCount")); n != nil {
		detail = itoa(*n) + " messages · not retained"
	}
	r := ContentRow{Label: "request body", Detail: detail}
	if !w.Up.IsNone() {
		n, _ := w.Up.Int64()
		r.Size, r.Unit = &n, sp("bytes")
	}
	return []ContentRow{r}
}

func receivedShape(w *Wire, md Value) []ContentRow {
	out := []ContentRow{}
	if n := pyInt(md.Get("thinkingContentLength")); n != nil && *n != 0 {
		out = append(out, ContentRow{Label: "thinking", Detail: "not retained", Size: n, Unit: sp("chars")})
	}
	if n := pyInt(md.Get("textContentLength")); n != nil && *n != 0 {
		out = append(out, ContentRow{Label: "text", Detail: "not retained", Size: n, Unit: sp("chars")})
	}
	for _, name := range toolCounts(w) {
		out = append(out, ContentRow{Label: "tool_use · " + name, Detail: "not retained", Tone: sp("tool")})
	}
	if len(out) == 0 {
		r := ContentRow{Label: "response body", Detail: "not retained"}
		if !w.Down.IsNone() {
			n, _ := w.Down.Int64()
			r.Size, r.Unit = &n, sp("bytes")
		}
		out = append(out, r)
	}
	return out
}

// usageMissing is rows.usage_missing.
func usageMissing(output *int64, status Value, stop Value, wsTurn bool, down Value) Value {
	if output != nil {
		return None
	}
	if stop.Truthy() && strings.Contains(stop.Str(), "interrupt") {
		return StrV("interrupted")
	}
	if status.K == Int {
		if n, _ := status.Int64(); n >= 400 {
			return StrV("failed")
		}
	}
	if !wsTurn && !down.IsNone() {
		if n, _ := down.float(); n > 512*1024 {
			return StrV("parseBuffer")
		}
	}
	return StrV("streamCut")
}

// --- RequestDetail -------------------------------------------------------------

// call is one drawer's derivation, kept for its details.
type call struct {
	detail Value
	w      *Wire
	sid    Value
	f      facts
	pos    position
	md     Value
	tokens tokens
}

// sessionOf is w.session_id.
func (s *scope) sessionOf(w *Wire) Value {
	v := w.ReqHeaders.Get("x-claude-code-session-id").Or(w.C("session_id"))
	if v.Truthy() {
		return v
	}
	if w.Kind() == "analytics" {
		return s.batchSession(w.CID)
	}
	return v.Or(None)
}

func (s *scope) detailOf(w *Wire) *call {
	sid := s.sessionOf(w)
	var x *sess
	if sid.Truthy() {
		x = s.session(strOf(sid))
		if !x.exists {
			x = nil
		}
	}
	f := s.factsOf(sid, w.requestID(), w.clientRequestID(), w)
	pos := s.positionOf(x, w)
	md := Value{K: Object}
	if f.success != nil {
		md = f.success.MD()
	}
	model := w.Kind() == "model"
	t := tokensOf(w, md)
	retry := retryInfo(w, f, pos)
	status := w.Status
	if status.IsNone() && !w.EndTS.IsNone() {
		status = StrV("ERR")
	}
	failed := w.failed()
	var sent []ContentRow
	if w.Retained && w.HasSentRetained && len(w.SentRetained) > 0 {
		sent = w.SentRetained
	} else if model {
		sent = sentShape(w, md)
	} else {
		sent = []ContentRow{}
	}
	var received []ContentRow
	switch {
	case failed:
		detail := "HTTP " + w.Status.Str()
		if e := w.errorV(); e.Truthy() {
			detail = e.Str()
		}
		received = []ContentRow{{Label: "no response", Detail: detail}}
	case w.Retained:
		received = w.ReceivedRetained
	case model:
		received = receivedShape(w, md)
	default:
		received = []ContentRow{}
	}
	clientTTFT := pyFloat(md.Get("ttftMs"))
	var proxyTTFT *float64
	if !failed {
		proxyTTFT = msBetween(IntV(w.TS), w.DownFirst.Or(w.RespTS))
	}
	source := None
	switch {
	case clientTTFT != nil:
		source = StrV("client")
	case proxyTTFT != nil:
		source = StrV("proxy")
	}
	estimated := int64(0)
	if model && f.success == nil && !failed {
		estimated = 1
	}
	um := None
	if model {
		st := None
		if w.Status.K == Int {
			st = w.Status
		}
		um = usageMissing(t.output, st, w.C("stop_reason"), strings.Contains(w.CID, "~"), w.Down)
	}
	streamEvents := None
	if n := pyInt(md.Get("streamEventCount")); n != nil && *n != 0 {
		streamEvents = IntV(*n)
	} else if w.SSECount > 0 {
		streamEvents = IntV(int64(w.SSECount))
	}
	tc := list()
	for _, n := range toolCounts(w) {
		tc.L = append(tc.L, StrV(n))
	}
	kind := "normal"
	if failed {
		kind = "failed"
	}
	ks := obj(
		"model", w.C("model").Or(vStr(pyStr(md.Get("model")))),
		"effort", w.C("effort").Or(vStr(pyStr(md.Get("effort_level")))),
		"thinking", thinking(w.C("thinking_type")),
		"tokens", t.value(),
		"cachedPct", vFloat(cachedPct(t)),
		"cost", obj("usd", vFloat(pyFloat(md.Get("costUSD"))), "basis", StrV("api-equivalent"), "estimatedCalls", IntV(estimated)),
		"ttftMs", vFloat(clientTTFT),
		"proxyTtftMs", vFloat(proxyTTFT),
		"ttftSource", source,
		"reasoningTokens", vInt(pyInt(w.C("reasoning_tokens"))),
		"usageMissing", um,
		"streamEvents", streamEvents,
		"stopReason", w.C("stop_reason").Or(vStr(pyStr(md.Get("stop_reason")))),
		"toolCalls", tc,
	)
	d := obj(
		"requestId", StrV(w.CID),
		"kind", StrV(kind),
		"t", dt(w.TS),
		"method", w.Method,
		"path", StrV(w.Path),
		"status", status,
		"durationMs", vFloat(msBetween(IntV(w.TS), w.EndTS)),
		"bytesUp", w.Up,
		"bytesDown", w.Down,
		"sessionId", sid,
		"promptIndex", vInt(pos.promptIndex),
		"stepIndex", vInt(pos.stepIndex),
		"stepCount", vInt(pos.stepCount),
		"requestClass", w.requestClass(),
		"prevRequestId", pos.prev,
		"nextRequestId", pos.next,
		"keyStrip", ks,
		"crossCheck", obj("matched", BoolV(f.success != nil), "costAgrees", None),
		"notice", notice(w, retry),
		"retry", retry,
		"sent", contentRows(sent),
		"received", contentRows(received),
		"contentRetained", BoolV(w.Retained),
	)
	return &call{detail: d, w: w, sid: sid, f: f, pos: pos, md: md, tokens: t}
}

// findReport is request_detail.find_report: the session and tengu_api_success
// a reported:<requestId | clientRequestId | ts> id names.
func (s *scope) findReport(rid string) (string, *ev) {
	key := strings.TrimPrefix(rid, "reported:")
	if s.reports == nil {
		s.reports = map[string]*ev{}
		for _, e := range s.events {
			if e.name != "tengu_api_success" {
				continue
			}
			for _, k := range []string{extractString(e.mdJSON, "requestId"), extractString(e.mdJSON, "clientRequestId"), itoa(e.ts)} {
				if _, ok := s.reports[k]; !ok {
					s.reports[k] = e
				}
			}
		}
	}
	first := s.reports[key]
	if first == nil || first.session == "" {
		return "", nil
	}
	sid := first.session
	for _, e := range s.session(sid).success {
		for _, v := range []*string{e.mdStr("requestId"), e.mdStr("clientRequestId")} {
			if v != nil && *v == key {
				return sid, e
			}
		}
		if itoa(e.ts) == key {
			return sid, e
		}
	}
	return "", nil
}

// extractString is DuckDB's json_extract_string(md, '$.<key>'): the first of a
// repeated key; a number or an object as its JSON text; "\x00" for NULL (no
// such key, a JSON null, or no object).
func extractString(md, key string) string {
	const null = "\x00"
	if md == "" {
		return null
	}
	dec := json.NewDecoder(strings.NewReader(md))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return null
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return null
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return null
		}
		if k, _ := t.(string); k != key {
			continue
		}
		v, ok := Loads(raw)
		switch {
		case !ok, v.K == Null:
			return null
		case v.K == Str:
			return v.S
		}
		return string(raw)
	}
	return null
}

func (s *scope) reportedDetail(rid string) *call {
	sid, e := s.findReport(rid)
	if e == nil {
		return nil
	}
	x := s.session(sid)
	md := e.MD()
	t := tokensOf(nil, md)
	in, out := pyInt(md.Get("inputTokens")), pyInt(md.Get("outputTokens"))
	cached := pyInt(md.Get("cachedInputTokens"))
	usage := "usage not reported"
	if in != nil && out != nil {
		usage = commaInt(*in) + " in"
		if cached != nil {
			usage += " (" + commaInt(*cached) + " cached)"
		}
		usage += " → " + commaInt(*out) + " out"
	}
	var promptIndex *int64
	if x.exists {
		if n, ok := x.index[e.prompt.Or(StrV("")).Str()]; ok {
			promptIndex = i64(n)
		}
	}
	ttft := pyFloat(md.Get("ttftMs"))
	source := None
	if ttft != nil {
		source = StrV("client")
	}
	usageRow := ContentRow{Label: "usage", Detail: usage, Size: out}
	if out != nil {
		usageRow.Unit = sp("tokens")
	}
	ks := obj(
		"model", vStr(pyStr(md.Get("model"))),
		"effort", vStr(pyStr(md.Get("effort_level"))),
		"thinking", None,
		"tokens", t.value(),
		"cachedPct", vFloat(cachedPct(t)),
		"cost", obj("usd", vFloat(pyFloat(md.Get("costUSD"))), "basis", StrV("api-equivalent"), "estimatedCalls", IntV(0)),
		"ttftMs", vFloat(ttft),
		"proxyTtftMs", None,
		"ttftSource", source,
		"reasoningTokens", None,
		"usageMissing", None,
		"streamEvents", vInt(pyInt(md.Get("streamEventCount"))),
		"stopReason", vStr(pyStr(md.Get("stop_reason"))),
		"toolCalls", list(),
	)
	d := obj(
		"requestId", StrV(rid),
		"kind", StrV("notCaptured"),
		"t", dt(e.ts),
		"method", None,
		"path", StrV("/v1/messages"),
		"status", None,
		"durationMs", vFloat(pyFloat(md.Get("durationMs"))),
		"bytesUp", None,
		"bytesDown", None,
		"sessionId", StrV(sid),
		"promptIndex", vInt(promptIndex),
		"stepIndex", None,
		"stepCount", None,
		"requestClass", None,
		"prevRequestId", None,
		"nextRequestId", None,
		"keyStrip", ks,
		"crossCheck", obj("matched", BoolV(false), "costAgrees", None),
		"notice", obj("tone", StrV("danger"), "title", StrV("▲ This call bypassed the proxy"), "body", StrV(
			"Claude Code’s own analytics (tengu_api_success) report a model call the proxy never saw. "+
				"Tokens, cost and timing come from Claude Code’s report; there is no request or response to show.")),
		"retry", None,
		"sent", contentRows([]ContentRow{{Label: "messages", Detail: "not captured"}}),
		"received", contentRows([]ContentRow{usageRow}),
		"contentRetained", BoolV(false),
	)
	return &call{detail: d, sid: StrV(sid), f: facts{success: e, events: []*ev{e}}, md: md, tokens: t}
}
