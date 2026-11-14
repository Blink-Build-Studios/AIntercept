package drawer

import (
	"sort"
	"strconv"
	"strings"
)

// request_detail.details: everything known about one request.

// completeFields are the fields the observer can write on a complete row;
// absent ones are listed as "not on rows yet" (request_detail.COMPLETE_FIELDS).
var completeFields = []string{"model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"stop_reason", "message_id", "previous_message_id", "thread_type", "stream", "max_tokens", "effort", "thinking_type",
	"service_tier", "inference_geo", "speed", "tools_available", "tool_uses", "account_uuid", "device_id", "session_id",
	"provider_org_id"}

var contentKeys = map[string]bool{"body_b64": true, "body_preview": true, "headers": true}

func (s *scope) detailsOf(c *call) Value {
	d, w, f, pos, md := c.detail, c.w, c.f, c.pos, c.md
	k := d.Get("keyStrip")
	t := k.Get("tokens")
	matched := d.Get("crossCheck").Get("matched").B
	rh := None
	if w != nil {
		rh = w.RespHeaders
	}
	stalls := None
	switch {
	case f.stall != nil:
		stalls = vInt(pyInt(f.stall.get("stall_count")))
	case f.success != nil:
		stalls = IntV(0)
	}
	var sandbox *string
	if c.sid.Truthy() {
		x := s.session(strOf(c.sid))
		var tns int64
		if w != nil {
			tns = w.TS
		} else if f.success != nil {
			tns = f.success.ts
		}
		sandbox = x.sandboxUpTo(floorUS(tns) + ns)
	}
	tools := None
	if w != nil {
		tools = w.C("tools_available")
	}
	var mcp []string
	if tools.K == List {
		seen := map[string]bool{}
		for _, x := range tools.L {
			if x.K == Object && x.Get("server").Truthy() {
				name := x.Get("server").Str()
				if !seen[name] {
					seen[name] = true
					mcp = append(mcp, name)
				}
			}
		}
		sort.Strings(mcp)
	}
	wc := func(key string) Value {
		if w == nil {
			return None
		}
		return w.C(key)
	}
	mdInt := func(key string) Value { return vInt(pyInt(md.Get(key))) }
	okTone := ""
	if matched {
		okTone = "ok"
	}
	maxTokens := None
	if w != nil {
		maxTokens = vInt(pyInt(w.C("max_tokens")))
	}
	stream := None
	if w != nil && w.Complete.Truthy() && w.Complete.Has("stream") {
		stream = w.C("stream")
	}
	toolsSetting := None
	if md.Truthy() {
		toolsSetting = mdInt("toolsCount")
	} else if tools.Truthy() {
		toolsSetting = IntV(int64(tools.Len()))
	}
	toolsUsable := mdInt("toolsCount")
	if tools.Truthy() {
		toolsUsable = IntV(int64(tools.Len()))
	}
	mcpCount := None
	if tools.Truthy() {
		mcpCount = IntV(int64(len(mcp)))
	}
	var mcpNote *string
	if len(mcp) > 0 {
		mcpNote = sp(strings.Join(mcp, ", "))
	}
	status, requestID, ip, keyHash := None, None, None, None
	if w != nil {
		status, requestID, ip, keyHash = w.Status, w.requestID(), w.IP, w.KeyHash
	}
	requestID = requestID.Or(vStr(pyStr(md.Get("requestId"))))
	retries := None
	if !md.Get("attempt").IsNone() {
		n := int64(1)
		if a := pyInt(md.Get("attempt")); a != nil && *a != 0 {
			n = *a
		}
		retries = IntV(n - 1)
	}
	tls := None
	if w != nil {
		tls = StrV("intercepted by the proxy")
	}
	sandboxV := None
	if sandbox != nil {
		switch *sandbox {
		case "true":
			sandboxV = StrV("on")
		case "false":
			sandboxV = StrV("off")
		}
	}
	retention := "shape only"
	if d.Get("contentRetained").B {
		retention = "full content (experimentation mode)"
	}
	sections := list(
		section("Tokens & cost", StrV("count"),
			row("input · uncached", t.Get("input"), "tokens"),
			row("cache read", t.Get("cacheRead"), "tokens"),
			row("cache write", t.Get("cacheWrite"), "tokens"),
			row("cache write 5m", t.Get("cacheWrite5m"), "tokens"),
			row("cache write 1h", t.Get("cacheWrite1h"), "tokens"),
			row("output", t.Get("output"), "tokens"),
			row("thinking", mdInt("thinkingContentLength"), "chars"),
			row("costUSD (Claude Code)", k.Get("cost").Get("usd"), "usd").tone(okTone),
		),
		section("Request settings", None,
			row("model", k.Get("model"), ""),
			row("max_tokens", maxTokens, "tokens"),
			row("thinking", wc("thinking_type"), ""),
			row("effort", k.Get("effort"), ""),
			row("stream", stream, ""),
			row("tools", toolsSetting, ""),
		),
		section("Thread position", None,
			row("previous step", pos.prevMessage.Or(pos.prev), ""),
			row("this step", wc("message_id"), ""),
			row("next step", pos.nextMessage.Or(pos.next), ""),
			row("thread type", wc("thread_type"), ""),
			row("messages", mdInt("messageCount"), ""),
			row("query depth", mdInt("queryDepth"), ""),
		),
		section("What the model could use", StrV("skills"),
			row("tools", toolsUsable, ""),
			row("deferred MCP tools", mdInt("deferredToolsCount"), ""),
			row("MCP servers", mcpCount, "").note(mcpNote),
			row("system prompt", mdInt("systemCharLength"), "chars"),
			row("tool schemas", mdInt("toolsCharLength"), "chars"),
			row("skills", None, "").note(sp("not extracted yet")),
		),
		section("Response service", None,
			row("service tier", wc("service_tier"), ""),
			row("inference geo", wc("inference_geo"), ""),
			row("speed", wc("speed"), ""),
			row("status", status, ""),
			row("request-id", requestID, ""),
			row("retries", retries, ""),
			row("streaming stalls", stalls, ""),
		),
		section("Account limits at response", None,
			row("unified status", limit(rh, "status"), ""),
			row("7-day utilization", pct(limit(rh, "7d-utilization")), "pct"),
			row("representative claim", limit(rh, "representative-claim"), ""),
			row("overage", limit(rh, "overage-status"), ""),
			row("overage disabled reason", limit(rh, "overage-disabled-reason"), ""),
			row("fallback percentage", pct(limit(rh, "fallback-percentage")), "pct"),
		),
		section("Network & proxy", None,
			row("client", ip, ""),
			row("device", wc("device_id"), ""),
			row("TLS", tls, ""),
			row("upstream connect", None, "ms").note(sp("not recorded by the proxy")),
			row("bytes up", d.Get("bytesUp"), "bytes"),
			row("bytes down", d.Get("bytesDown"), "bytes"),
			row("credential", keyHash, "").tone("muted").note(sp("key_hash (refreshes; not identity)")),
		),
		section("Cross-check & posture", None,
			row("analytics match", BoolV(matched), "").tone(okTone),
			row("cost agreement", None, "").note(sp("no price table (costUSD is Claude Code’s own)")),
			row("ttft (Claude Code)", k.Get("ttftMs"), "ms"),
			row("ttft (proxy)", k.Get("proxyTtftMs"), "ms"),
			row("permissionMode", vStr(pyStr(md.Get("permissionMode"))), ""),
			row("sandbox", sandboxV, ""),
			row("content retention", StrV(retention), "").tone("muted"),
		),
	)
	if w != nil && w.C("client_metadata").K == Object {
		sections.L = append(sections.L, codexSection(w))
	}
	proxy := list()
	if w != nil {
		end := w.EndTS.Or(w.DownLast)
		proxy.L = append(proxy.L, phase("accept + TLS", None, None), phase("upstream connect", None, None))
		first := w.DownFirst.Or(w.RespTS)
		ts := IntV(w.TS)
		orZero := func(f *float64) Value {
			if f == nil || *f == 0 {
				return FloatV(0)
			}
			return FloatV(*f)
		}
		if first.Truthy() {
			proxy.L = append(proxy.L, phase("first byte", FloatV(0), orZero(msBetween(ts, first))))
			if end.Truthy() {
				e, _ := end.Int64()
				fi, _ := first.Int64()
				if e > fi {
					proxy.L = append(proxy.L, phase("streaming", orZero(msBetween(ts, first)), orZero(msBetween(first, end))))
				}
			}
		}
		if end.Truthy() {
			proxy.L = append(proxy.L, phase("total", FloatV(0), orZero(msBetween(ts, end))))
		}
	}
	var client []DetailRow
	for _, key := range []string{"requestPrepareMs", "ttftMs", "firstContentMs", "durationMs", "durationMsIncludingRetries", "attempt"} {
		if md.Get(key).IsNone() {
			continue
		}
		if key == "attempt" {
			client = append(client, row(key, vInt(pyInt(md.Get(key))), ""))
		} else {
			client = append(client, row(key, vFloat(pyFloat(md.Get(key))), "ms"))
		}
	}
	events := list()
	if c.sid.Truthy() {
		for _, e := range f.events {
			events.L = append(events.L, s.eventRow(e))
		}
	}
	return obj(
		"composition", t,
		"sections", sections,
		"tool", s.toolDetails(w, f, pos),
		"timing", obj("proxy", proxy, "client", detailRows(client)),
		"events", events,
		"fields", fieldsOf(w, md),
	)
}

func floorUS(n int64) int64 {
	us := n / 1000
	if n%1000 != 0 && n < 0 {
		us--
	}
	return us * 1000
}

func section(title string, accent Value, rows ...DetailRow) Value {
	return obj("title", StrV(title), "accent", accent, "rows", detailRows(rows))
}

func phase(label string, start, dur Value) Value {
	return obj("label", StrV(label), "startMs", start, "durMs", dur)
}

func limit(h Value, name string) Value { return header(h, "anthropic-ratelimit-unified-"+name) }

// pct is request_detail._pct.
func pct(v Value) Value {
	f := pyFloat(v)
	if f == nil {
		return None
	}
	return FloatV(round1(*f * 100))
}

// eventRow is the session's rendered event_rows row of an event, as the API's
// EventRow (request_detail._event_rows: session.py's link, summary and marker).
func (s *scope) eventRow(e *ev) Value {
	r := s.rows[e.id]
	return obj("eventId", StrV(e.id), "t", dt(e.ts), "name", StrV(e.name), "promptIndex", vInt(r.PromptIndex),
		"linkedTo", obj("kind", StrV(r.LinkKind), "id", StrV(r.LinkID), "label", StrV(r.LinkLabel)),
		"summary", StrV(r.Summary), "marker", optS(r.Marker), "internal", BoolV(r.Internal), "collapsedCount", None)
}

// tool is session._tool's ToolCallData, the fields the drawer shows.
type tool struct {
	id          Value
	name        string
	mcpServer   Value
	ok          bool
	durationMS  *float64
	exitCode    *int64
	resultBytes *int64
	destructive *string
}

func toolOf(e *ev, j *joins) tool {
	tid := pyStr(e.get("toolUseID"))
	var bash *ev
	if tid != nil && *tid != "" {
		bash = j.bash[*tid]
	}
	t := tool{id: vStr(tid), name: "—", ok: e.name == "tengu_tool_use_success",
		durationMS: pyFloat(e.get("durationMs")), resultBytes: pyInt(e.get("toolResultSizeBytes"))}
	name := pyStr(e.get("toolName"))
	if name != nil && *name != "" {
		t.name = *name
	}
	if srv := pyStr(e.get("mcpServerName")); srv != nil && *srv != "" {
		t.mcpServer = StrV(*srv)
	} else if n := deref(name); strings.HasPrefix(n, "mcp__") {
		if parts := strings.Split(n, "__"); len(parts) >= 3 {
			t.mcpServer = StrV(parts[1])
		}
	}
	if bash != nil {
		t.exitCode = pyInt(bash.get("exit_code"))
		if cat := pyStr(bash.get("destructive_category")); cat != nil && *cat != "none" {
			t.destructive = cat
		}
	}
	return t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// toolDetails is request_detail._tool_details.
func (s *scope) toolDetails(w *Wire, f facts, pos position) Value {
	nxt := -1
	if pos.next.K == Str {
		for i, x := range pos.calls {
			if x.id == pos.next.S {
				nxt = i
				break
			}
		}
	}
	out := list()
	for _, e := range f.tools {
		t := toolOf(e, f.joins)
		var input []DetailRow
		if w != nil {
			if b := w.toolInput(t.id); b != nil && b.IsDict {
				input = b.Rows
			}
		}
		status := "error"
		if t.ok {
			status = "ok"
		}
		exec := []DetailRow{
			row("status", StrV(status), ""),
			row("duration", vFloat(t.durationMS), "ms"),
			row("PreToolUse hook", vFloat(pyFloat(e.get("preToolHookDurationMs"))), "ms"),
			row("permission wait", vFloat(pyFloat(e.get("permissionDurationMs"))), "ms"),
		}
		if t.exitCode != nil {
			exec = append(exec, row("exit code", IntV(*t.exitCode), ""))
		}
		if t.destructive != nil {
			exec = append(exec, row("destructive", StrV(*t.destructive), ""))
		}
		delivered := None
		if nxt >= 0 {
			delivered = StrV("step " + strconv.Itoa(nxt+1))
		}
		var nextNote *string
		if pos.next.K == Str {
			nextNote = sp(pos.next.S)
		}
		result := []DetailRow{
			row("delivered in", delivered, "").note(nextNote),
			row("size", vInt(t.resultBytes), "bytes"),
			row("is_error", BoolV(!t.ok), ""),
		}
		out.L = append(out.L, obj("toolUseId", t.id, "name", StrV(t.name), "mcpServer", t.mcpServer,
			"input", detailRows(input), "execution", detailRows(exec), "result", detailRows(result)))
	}
	if len(out.L) == 0 && w != nil {
		for _, name := range toolCounts(w) {
			out.L = append(out.L, obj("toolUseId", None, "name", StrV(name), "mcpServer", None, "input", list(),
				"execution", list(), "result", list()))
		}
	}
	return out
}

// fieldsOf is request_detail._fields: every field on the request's captured
// rows, flattened.
func fieldsOf(w *Wire, md Value) Value {
	out := list()
	if w != nil {
		c := Value{K: Object}
		for _, m := range w.Complete.O {
			if !contentKeys[m.Key] {
				c.O = append(c.O, m)
			}
		}
		for _, k := range completeFields {
			if !c.Has(k) {
				c.O = append(c.O, Member{k, None})
			}
		}
		flatten("", c, &out)
	}
	if md.Truthy() {
		flatten("analytics", md, &out)
	}
	return out
}

func flatten(prefix string, v Value, out *Value) {
	switch v.K {
	case Object:
		for _, m := range sortedMembers(v.O) {
			k := m.Key
			if prefix != "" {
				k = prefix + "." + m.Key
			}
			flatten(k, m.Val, out)
		}
	case List:
		out.L = append(out.L, obj("key", StrV(prefix), "value", StrV(redact(Dumps(v, ",", ":", -1)))))
	case Null:
		out.L = append(out.L, obj("key", StrV(prefix), "value", None))
	case Bool:
		s := "false"
		if v.B {
			s = "true"
		}
		out.L = append(out.L, obj("key", StrV(prefix), "value", StrV(redact(s))))
	default:
		out.L = append(out.L, obj("key", StrV(prefix), "value", StrV(redact(v.Str()))))
	}
}

// codexSection is request_detail._codex_section.
func codexSection(w *Wire) Value {
	md := w.C("client_metadata").Or(Value{K: Object})
	plan, windows, ok := codexLimits(w.RespHeaders)
	var shown []string
	for _, x := range windows {
		if x.used != nil {
			shown = append(shown, x.name+" "+formatG(*x.used)+"%")
		}
	}
	limits := None
	if len(shown) > 0 {
		limits = StrV(strings.Join(shown, " · "))
	}
	planV := None
	if ok {
		planV = plan
	}
	ms := func(k string) Value { return vStr(pyStr(md.Get(k))) }
	return section("Codex · response", None,
		row("request kind", ms("request_kind"), ""),
		row("thread source", ms("thread_source"), ""),
		row("agent", ms("agent_name"), ""),
		row("history", ms("history_initialization"), ""),
		row("trigger", ms("turn_trigger"), ""),
		row("sandbox", ms("sandbox_mode"), ""),
		row("limits", limits, ""),
		row("plan", planV, ""),
	)
}

type limitWindow struct {
	name string
	used *float64
}

// codexLimits is agents.codex_limits (the plan and the windows; ok false where
// it returns None).
func codexLimits(h Value) (Value, []limitWindow, bool) {
	if h.K != Object {
		h = Value{K: Object}
	}
	var windows []limitWindow
	for _, w := range []string{"primary", "secondary"} {
		minutes := h.Get("x-codex-" + w + "-window-minutes")
		if !h.Has("x-codex-"+w+"-used-percent") || minutes.K == Str && (minutes.S == "0" || minutes.S == "") {
			continue
		}
		windows = append(windows, limitWindow{name: windowName(minutes, w), used: codexPct(h.Get("x-codex-" + w + "-used-percent"))})
	}
	plan := h.Get("x-codex-plan-type")
	if len(windows) == 0 && !plan.Truthy() {
		return None, nil, false
	}
	return plan, windows, true
}

func codexPct(v Value) *float64 {
	if v.IsNone() || v.K == Str && v.S == "" {
		return nil
	}
	return pyFloat(v)
}

func windowName(minutes Value, fallback string) string {
	s := ""
	if minutes.Truthy() {
		s = minutes.Str()
	}
	switch s {
	case "300":
		return "5h"
	case "10080":
		return "week"
	}
	n, ok := pyIntStr(s)
	if !ok {
		return fallback
	}
	if n%1440 != 0 {
		return strconv.FormatInt(floorDivInt(n, 60), 10) + "h"
	}
	return strconv.FormatInt(floorDivInt(n, 1440), 10) + "d"
}

// pyIntStr is int(str).
func pyIntStr(s string) (int64, bool) {
	s = strip(s)
	if strings.Contains(s, "_") {
		for i := range len(s) {
			if s[i] == '_' && (i == 0 || i == len(s)-1 || !isDigit(s[i-1]) || !isDigit(s[i+1])) {
				return 0, false
			}
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "+"), 10, 64)
	return n, err == nil && !strings.HasPrefix(s, "+-")
}

func floorDivInt(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
