package process

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The session_rows snapshot: one row per session, every value of its session
// row, its timeline band and its page header that doesn't depend on the time of
// the request, as the query API showed them (claude_code.py load_sessions and
// its rollups, rows.session_row, timeline.band, session.header_facts,
// agents.workspace, codex_facts and latest_codex_limits). Built with the request
// rows, from the same parts (requestrows.go), and held equal to the API's output
// by the same parity test (docs/architecture.md, Decision 4).
//
// What depends on now is left to the API: live is now - last_ns <= 5 minutes;
// a live session's end is null and its open interval ends now.

// sessionEventNames is the Claude Code events the API's Session holds (claude_code.py
// _EVENT_NAMES): the device comes from the first of them that names one.
var sessionEventNames = map[string]bool{
	"tengu_api_success": true, "tengu_api_query": true, "tengu_input_prompt": true, "tengu_cancel": true,
	"tengu_bash_tool_command_executed": true, "tengu_return_to_session": true, "tengu_event_loop_stall": true,
}

// titleWithin is claude_code.py TITLE_WITHIN_NS: a Codex title thread names the
// same account's thread that started last before it, within this window.
const titleWithin = 5 * 60 * int64(time.Second)

// gapSplit is claude_code.py GAP_SPLIT_NS: coverage windows split at gaps this long.
const gapSplit = 15 * 60 * int64(time.Second)

// sessEvents is what a session's analytics events give its row, folded in table order.
type sessEvents struct {
	events    int64 // Claude Code events (first is set when there are any)
	first     int64
	account   string // the earliest event's that names one (the least, at the same time)
	accountNS int64
	device    string // the earliest of sessionEventNames' that names one
	deviceTS  int64
	env       [3]envPick // platform, terminal, shell: each the newest event's that has it
	queries   int64      // tengu_api_query with a permission mode
	bypass    int64      // … bypassPermissions
	negative  int64      // tengu_input_prompt flagged is_negative
	cancels   int64      // tengu_cancel
	destroy   int64      // tengu_bash_tool_command_executed with a destructive category
	turnMD    string     // the newest codex_turn_event's metadata
	turnTS    int64
	turnFound bool

	// For the person rollups (personmap.go).
	devices  map[string]bool // the devices sessionEventNames' events name
	sandbox  newest          // the newest tengu_bash_tool_command_executed's sandbox_enabled
	customCA bool            // a tengu_api_success's gzipSkipReason is 'custom_ca'
}

type envPick struct {
	ts int64
	v  string
	ok bool
}

// before reports whether (ts, v) is newer than the pick: the latest, the greatest
// value at the same time (arg_max over (ts, value)), whatever order events come in.
func (p envPick) before(ts int64, v string) bool {
	return !p.ok || ts > p.ts || ts == p.ts && v > p.v
}

var envKeys = [3]string{"platform", "terminal", "shell"}

func (a *eventAgg) sessOf(s string) *sessEvents {
	x := a.sess[s]
	if x == nil {
		x = &sessEvents{}
		a.sess[s] = x
	}
	return x
}

// add folds in one of the session's Claude Code events.
func (x *sessEvents) add(e eventCols) {
	if x.events == 0 || e.NS < x.first {
		x.first = e.NS
	}
	x.events++
	if earlier(e.NS, e.PersonID, x.accountNS, x.account) {
		x.account, x.accountNS = e.PersonID, e.NS
	}
	if e.DeviceID != "" && sessionEventNames[e.Name] {
		if earlier(e.NS, e.DeviceID, x.deviceTS, x.device) {
			x.device, x.deviceTS = e.DeviceID, e.NS
		}
		if x.devices == nil {
			x.devices = map[string]bool{}
		}
		x.devices[e.DeviceID] = true
	}
	if e.EnvJSON != "" && x.envMayChange(e.NS) {
		md := jsonFields(e.EnvJSON, envKeys[:]...)
		for i, k := range envKeys {
			if v, ok := jsonString(md[k]); ok && x.env[i].before(e.NS, v) {
				x.env[i] = envPick{e.NS, strings.Clone(v), true}
			}
		}
	}
	switch e.Name {
	case "tengu_api_query":
		if m, _ := jsonString(jsonFields(e.MetadataJSON, "permissionMode")["permissionMode"]); m != "" {
			x.queries++
			if m == "bypassPermissions" {
				x.bypass++
			}
		}
	case "tengu_input_prompt":
		if v, _ := jsonString(jsonFields(e.MetadataJSON, "is_negative")["is_negative"]); v == "true" {
			x.negative++
		}
	case "tengu_cancel":
		x.cancels++
	case "tengu_bash_tool_command_executed":
		if v, _ := jsonString(jsonFields(e.MetadataJSON, "destructive_category")["destructive_category"]); v != "" && v != "none" {
			x.destroy++
		}
	}
}

// addSums folds in what the person rollups read of one of the session's events.
func (x *sessEvents) addSums(ts int64, e *eventSums) {
	if e.sandbox != nil {
		x.sandbox.offer(ts, *e.sandbox)
	}
	x.customCA = x.customCA || e.gzipCA
}

// envMayChange: an event at ts could still set one of the env fields.
func (x *sessEvents) envMayChange(ts int64) bool {
	for _, p := range x.env {
		if !p.ok || ts >= p.ts { // at the same time, a greater value may win
			return true
		}
	}
	return false
}

// addTurn folds in one of a Codex thread's codex_turn_event (arg_max(md, ts)).
func (x *sessEvents) addTurn(e eventCols) {
	if e.MetadataJSON != "" && (!x.turnFound || e.NS > x.turnTS || e.NS == x.turnTS && e.MetadataJSON > x.turnMD) {
		x.turnMD, x.turnTS, x.turnFound = strings.Clone(e.MetadataJSON), e.NS, true
	}
}

// merge folds in a later part's.
func (x *sessEvents) merge(o *sessEvents) {
	if o.events > 0 && (x.events == 0 || o.first < x.first) {
		x.first = o.first
	}
	x.events += o.events
	if earlier(o.accountNS, o.account, x.accountNS, x.account) {
		x.account, x.accountNS = o.account, o.accountNS
	}
	if earlier(o.deviceTS, o.device, x.deviceTS, x.device) {
		x.device, x.deviceTS = o.device, o.deviceTS
	}
	for i, p := range o.env {
		if p.ok && x.env[i].before(p.ts, p.v) {
			x.env[i] = p
		}
	}
	x.queries += o.queries
	x.bypass += o.bypass
	x.negative += o.negative
	x.cancels += o.cancels
	x.destroy += o.destroy
	if o.turnFound && (!x.turnFound || o.turnTS > x.turnTS || o.turnTS == x.turnTS && o.turnMD > x.turnMD) {
		x.turnMD, x.turnTS, x.turnFound = o.turnMD, o.turnTS, true
	}
	for dev := range o.devices {
		if x.devices == nil {
			x.devices = map[string]bool{}
		}
		x.devices[dev] = true
	}
	x.sandbox = x.sandbox.merge(o.sandbox)
	x.customCA = x.customCA || o.customCA
}

// toolCount is rows.tool_calls' counts summed: int(count or 1) per tool.
func toolCount(toolUsesJSON string) int64 {
	if toolUsesJSON == "" {
		return 0
	}
	var uses []any
	if json.Unmarshal([]byte(toolUsesJSON), &uses) != nil {
		return 0
	}
	var n int64
	for _, u := range uses {
		m, ok := u.(map[string]any)
		if !ok {
			continue
		}
		c := int64(1)
		switch v := m["count"].(type) {
		case float64:
			if v != 0 {
				c = int64(v)
			}
		case string:
			if v != "" {
				if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
					c = i
				}
			}
		case bool:
			if v {
				c = 1
			}
		}
		n += c
	}
	return n
}

// sessionCalls is one session's calls, as the session rows read them.
type sessionCalls struct {
	visible []*rowCall // cc_calls, by time and id
	agent   []*rowCall // agent_calls (with the hidden ones), by time and id
}

// sessionRows builds every session's row: the sessions with a visible model call
// or a Claude Code event (the API's load_session). `sessions` is the sessions
// snapshot (origins and children).
func (d *rowData) sessionRows(sessions []Session) []SessionRow {
	by := map[string]*sessionCalls{}
	get := func(id string) *sessionCalls {
		x := by[id]
		if x == nil {
			x = &sessionCalls{}
			by[id] = x
		}
		return x
	}
	ccSessions := map[string]bool{}
	var titles []*rowCall // title calls with a text, of threads with no visible call
	for _, c := range d.calls {
		if !c.agentCall() {
			continue
		}
		if c.hidden == "" {
			ccSessions[c.session] = true
			x := get(c.session)
			x.visible = append(x.visible, c)
		}
	}
	for id := range d.eventCount {
		get(id)
	}
	for _, c := range d.calls {
		if !c.agentCall() {
			continue
		}
		if x := by[c.session]; x != nil {
			x.agent = append(x.agent, c)
		}
		if c.hidden == "title" && c.text != nil && !ccSessions[c.session] {
			titles = append(titles, c)
		}
	}
	byTime := func(cs []*rowCall) {
		sort.SliceStable(cs, func(i, j int) bool {
			return cs[i].ts < cs[j].ts || cs[i].ts == cs[j].ts && cs[i].id < cs[j].id
		})
	}
	byTime(titles)

	origins := map[string]*Session{}
	children := map[string][]*Session{}
	for i := range sessions {
		s := &sessions[i]
		origins[s.SessionID] = s
		if (s.OriginKind == "subagent" || s.OriginKind == "review") && s.OriginSessionID != "" {
			children[s.OriginSessionID] = append(children[s.OriginSessionID], s)
		}
	}

	ids := make([]string, 0, len(by))
	for id := range by {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([]SessionRow, 0, len(ids))
	for _, id := range ids {
		x := by[id]
		byTime(x.visible)
		byTime(x.agent)
		rows = append(rows, d.sessionRow(id, x, origins[id], children[id]))
	}
	d.attachTitles(rows, by, titles)
	for i := range rows {
		r := &rows[i]
		r.LabelText, r.LabelSource = sessionLabel(r.SessionID, r.Title)
		r.LabelTextAlone, r.LabelSourceAlone = sessionLabel(r.SessionID, r.TitleAlone)
	}
	return rows
}

// sessionLabel is rows.label: the thread's title, else the id.
func sessionLabel(id string, title *string) (string, string) {
	if title != nil && *title != "" {
		return *title, "title"
	}
	return "Session " + pyPrefix(id, 8), "none"
}

func (d *rowData) sessionRow(id string, x *sessionCalls, origin *Session, kids []*Session) SessionRow {
	ev := d.sess[id]
	if ev == nil {
		ev = &sessEvents{}
	}
	r := SessionRow{SessionID: id, ModelCalls: int64(len(x.visible)), Product: productClaudeCode}

	// start and last: the calls' and the Claude Code events' times.
	first, last, any := int64(0), int64(0), false
	if ev.events > 0 {
		first, last, any = ev.first, d.lastEvent[id], true
	}
	for _, c := range x.visible {
		if !any || c.ts < first {
			first = c.ts
		}
		if !any || c.ts > last {
			last = c.ts
		}
		any = true
	}
	r.StartNS, r.LastNS = first, last

	for _, c := range x.visible {
		if c.product != "" {
			r.Product = c.product
			break
		}
	}
	if len(x.visible) == 0 {
		r.Product = ""
	}
	r.Agent = agentOf(r.Product)
	codex := r.Product == productCodex
	for i := len(x.visible) - 1; i >= 0; i-- {
		if c := x.visible[i]; c.client != "" && r.Client == nil {
			r.Client = ptr(c.client)
		}
		if c := x.visible[i]; c.clientVersion != "" && r.ClientVersion == nil {
			r.ClientVersion, r.ClientVersionNS = ptr(c.clientVersion), ptr(c.ts)
		}
	}

	// Origin and children (the sessions snapshot).
	if o := origin; o != nil && (o.OriginKind != "" || o.Init == "cold_resume") {
		kind := o.OriginKind
		if kind == "" {
			kind = "resume"
		}
		r.OriginKind, r.OriginSessionID = ptr(kind), nz(o.OriginSessionID)
		switch kind {
		case "fork", "resume", "subagent", "review":
			oj := originJSON{Kind: kind, SessionID: nz(o.OriginSessionID), AgentName: nz(o.AgentName)}
			if o.OriginTurn != 0 {
				oj.Turn = ptr(o.OriginTurn)
			}
			switch o.Init {
			case "warm_fork", "cold_fork", "cold_resume":
				oj.Init = ptr(o.Init)
			}
			r.OriginJSON = ptr(marshalJSON(oj))
		}
	}
	if len(kids) > 0 {
		sort.SliceStable(kids, func(i, j int) bool {
			return kids[i].FirstNS < kids[j].FirstNS || kids[i].FirstNS == kids[j].FirstNS && kids[i].SessionID < kids[j].SessionID
		})
		cj := childrenJSON{SessionIDs: []string{}}
		for _, k := range kids {
			if k.OriginKind == "subagent" {
				cj.Subagent++
			} else {
				cj.Review++
			}
			cj.SessionIDs = append(cj.SessionIDs, k.SessionID)
		}
		r.ChildrenJSON = ptr(marshalJSON(cj))
	}

	// Hidden calls, and the title a thread's own title call gives it.
	for _, c := range x.agent {
		if c.hidden == "" {
			continue
		}
		r.HiddenCalls++
		if c.hidden == "title" && c.text != nil && *c.text != "" {
			r.Title = ptr(pyStripTitle(*c.text))
		}
	}
	r.TitleAlone = r.Title

	// Prompts, the first captured prompt's number, friction, bypass.
	prompts := map[string]bool{}
	for _, c := range x.visible {
		if c.prompt != "" {
			prompts[c.prompt] = true
		}
	}
	var minIdx *int64
	for _, e := range d.prompts[id] {
		if e.promptID != "" {
			prompts[e.promptID] = true
		} else {
			prompts["input:"+strconv.FormatInt(e.ts, 10)] = true
		}
		if e.index != nil && (minIdx == nil || *e.index < *minIdx) {
			minIdx = e.index
		}
	}
	if len(prompts) > 0 {
		r.Prompts = ptr(int64(len(prompts)))
	}
	r.PromptIDs = sortedKeys(prompts)
	if minIdx != nil && *minIdx > 1 {
		r.CapturedFromPrompt = ptr(*minIdx)
	}
	if ev.events > 0 {
		r.Events = ptr(ev.events)
	}
	r.Frustrated, r.Interrupted, r.Destructive = ev.negative, ev.cancels, ev.destroy
	if ev.queries > 0 {
		r.BypassPct = ptr(round1(float64(100*ev.bypass) / float64(ev.queries)))
	}

	// Coverage: each tengu_api_success against the captured calls.
	if ev.events == 0 {
		r.CovCaptured = int64(len(x.visible))
	} else {
		rids, crids := map[string]bool{}, map[string]bool{}
		for _, c := range x.visible {
			if c.reqID != "" {
				rids[c.reqID] = true
			}
			if c.clientReqID != "" {
				crids[c.clientReqID] = true
			}
		}
		var missed []int64
		reps := d.reports[id]
		for _, e := range reps {
			if !rids[e.reqID] && !crids[e.clientReqID] {
				missed = append(missed, e.ts)
			}
		}
		r.CovReported = ptr(int64(len(reps)))
		r.CovCaptured = int64(len(reps) - len(missed))
		r.CovNotCaptured = int64(len(missed))
		r.CovWindowFrom, r.CovWindowTo, r.CovWindowCount = coverageWindows(missed)
	}
	r.FieldsMissing = len(x.visible) > 0
	for _, c := range x.visible {
		if c.person != "" || c.device != "" {
			r.FieldsMissing = false
			break
		}
	}

	// The header: the account, device and networks.
	// The API's any_value: arbitrary among several, so the earliest event's, else call's.
	r.PersonID = nz(ev.account)
	if r.PersonID == nil {
		var ts int64
		for _, c := range x.visible {
			if earlier(c.ts, c.person, ts, deref(r.PersonID)) {
				r.PersonID, ts = ptr(c.person), c.ts
			}
		}
	}
	dev := ev.device
	if dev == "" {
		var ts int64
		for _, c := range x.visible {
			if earlier(c.ts, c.device, ts, dev) {
				dev, ts = c.device, c.ts
			}
		}
	}
	if dev != "" {
		r.DeviceID = ptr(dev)
		for i, p := range ev.env {
			if p.ok {
				v := p.v
				switch i {
				case 0:
					r.DevicePlatform = &v
				case 1:
					r.DeviceTerminal = &v
				case 2:
					r.DeviceShell = &v
				}
			}
		}
	}
	ips := map[string]bool{}
	for _, c := range x.visible {
		if c.ip != "" {
			ips[c.ip] = true
		}
	}
	if len(ips) > 0 {
		r.Networks = ptr(int64(len(ips)))
	}

	// Usage: the visible calls' tokens, tool calls and newest effort.
	var in, read, write, out, withOut, tools int64
	var anyTools bool
	effortTS := int64(math.MinInt64)
	for _, c := range x.visible {
		in, read, write, out = in+c.input, read+c.cacheRead, write+c.cacheWrite, out+c.output
		if c.output != 0 {
			withOut++
		}
		if c.hasTools {
			anyTools = true
			tools += c.toolCalls
		}
		if c.effort != "" && (r.Effort == nil || c.ts > effortTS) {
			r.Effort, effortTS = ptr(c.effort), c.ts
		}
	}
	r.TokensInput, r.TokensCacheRead, r.TokensCacheWrite, r.TokensOutput = nzi(in), nzi(read), nzi(write), nzi(out)
	r.OutputsKnown = withOut
	d.personFacts(&r, x, ev)
	switch n := int64(len(x.visible)); {
	case n > 0 && withOut == n:
		r.TokensAccuracy = "exact"
	case withOut == 0:
		r.TokensAccuracy = "missing"
	default:
		r.TokensAccuracy = "estimated"
	}
	if anyTools {
		r.ToolCalls = ptr(tools)
	}

	// A Codex thread's workspace, facts and limits.
	if codex {
		d.codexFacts(&r, x, ev)
	}
	return r
}

// personFacts is what the person pages roll up of the session (personmap.go).
func (d *rowData) personFacts(r *SessionRow, x *sessionCalls, ev *sessEvents) {
	devs, accts := map[string]bool{}, map[string]bool{}
	for dev := range ev.devices {
		devs[dev] = true
	}
	var rl *rateLimit
	for _, c := range x.visible { // by time and id: the newest is the last
		if c.device != "" {
			devs[c.device] = true
		}
		if c.person != "" {
			accts[c.person] = true
		}
		if c.cli != nil && (r.CLIVersionNS == nil || c.ts > *r.CLIVersionNS || c.ts == *r.CLIVersionNS && *c.cli > deref(r.CLIVersion)) {
			r.CLIVersion, r.CLIVersionNS = c.cli, ptr(c.ts)
		}
		if c.rl != nil && (rl == nil || c.rl.at >= rl.at) {
			rl = c.rl
		}
	}
	r.Devices, r.CallAccounts = sortedKeys(devs), sortedKeys(accts)
	r.Queries, r.Bypassed = ev.queries, ev.bypass
	if rl != nil {
		r.RLStatus, r.RLUtil, r.RLOverage, r.RLNS = ptr(rl.status), rl.util, rl.overage, ptr(rl.at)
	}
	if ev.sandbox.ok {
		r.Sandbox, r.SandboxNS = ptr(ev.sandbox.v), ptr(ev.sandbox.ts)
	}
	r.CustomCA = ev.customCA
}

type originJSON struct {
	Kind      string  `json:"kind"`
	SessionID *string `json:"sessionId"`
	Turn      *int64  `json:"turn"`
	Init      *string `json:"init"`
	AgentName *string `json:"agentName"`
}

type childrenJSON struct {
	Subagent   int64    `json:"subagent"`
	Review     int64    `json:"review"`
	SessionIDs []string `json:"sessionIds"`
}

// coverageWindows is claude_code.py windows: the times grouped into (from, to,
// count), split at gaps of 15 minutes or more.
func coverageWindows(ts []int64) (from, to, count []int64) {
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	for _, t := range ts {
		if n := len(to); n > 0 && t-to[n-1] < gapSplit {
			to[n-1] = t
			count[n-1]++
			continue
		}
		from, to, count = append(from, t), append(to, t), append(count, 1)
	}
	return from, to, count
}

// pyStripTitle is a title call's answer as the API keeps it: said.strip().strip('"').
func pyStripTitle(s string) string {
	return strings.Trim(strings.TrimFunc(s, pyIsSpace), `"`)
}

// pyIsSpace is Python's str.isspace for one character.
func pyIsSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029,
		0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// attachTitles is claude_code.py _attach_titles, for each Codex thread its own
// title call didn't name: a title thread (a title call in a thread of its own)
// names the same account's thread that started last before it, within five
// minutes. Title is that over every thread at once (a person's or the
// directory's load); TitleAlone over the thread alone (the session page's load,
// where a title the person's load gives a later thread can go to this one).
func (d *rowData) attachTitles(rows []SessionRow, by map[string]*sessionCalls, titles []*rowCall) {
	var codex []*SessionRow
	for i := range rows {
		r := &rows[i]
		if r.Product == productCodex && r.Title == nil && len(by[r.SessionID].visible) > 0 {
			codex = append(codex, r)
		}
	}
	if len(codex) == 0 {
		return
	}
	accounts := map[*SessionRow]map[string]bool{}
	all := map[string]bool{}
	for _, r := range codex {
		accounts[r] = map[string]bool{}
		for _, c := range by[r.SessionID].visible {
			if c.person != "" {
				accounts[r][c.person] = true
				all[c.person] = true
			}
		}
	}
	for _, t := range titles {
		if !all[t.person] || *t.text == "" {
			continue
		}
		said := pyStripTitle(*t.text)
		var pick *SessionRow
		for _, r := range codex {
			if accounts[r][t.person] && 0 <= t.ts-r.StartNS && t.ts-r.StartNS <= titleWithin {
				if r.TitleAlone == nil {
					r.TitleAlone = ptr(said)
				}
				if r.Title == nil && (pick == nil || r.StartNS > pick.StartNS) {
					pick = r
				}
			}
		}
		if pick != nil {
			pick.Title = ptr(said)
		}
	}
}

// codexFacts is a Codex thread's workspace (agents.workspace), facts
// (agents.codex_facts) and limits on its newest response (latest_codex_limits).
func (d *rowData) codexFacts(r *SessionRow, x *sessionCalls, ev *sessEvents) {
	var ext, sandbox *string
	var limits *rowCall
	extTS, sandboxTS := int64(math.MinInt64), int64(math.MinInt64)
	for _, c := range x.agent {
		if c.product != productCodex {
			continue
		}
		// Each the newest call's; at the same time, the greatest value (or call id).
		if c.ext != "" && (ext == nil || c.ts > extTS || c.ts == extTS && c.ext > *ext) {
			ext, extTS = ptr(c.ext), c.ts
		}
		if c.sandbox != nil && (sandbox == nil || c.ts > sandboxTS || c.ts == sandboxTS && *c.sandbox > *sandbox) {
			sandbox, sandboxTS = c.sandbox, c.ts
		}
		if c.provider != "" && (limits == nil || c.ts > limits.ts || c.ts == limits.ts && c.id > limits.id) {
			limits = c
		}
	}
	var cwd *string
	if ext != nil {
		ws := pyObject(*ext)["workspaces"]
		key, val, ok := pyFirstField(ws)
		if ok {
			cwd = ptr(key)
			if key != "" {
				r.Repo = ptr(pyBasename(key))
				if sha := pyObject(string(val))["latest_git_commit_hash"]; len(sha) > 0 && sha[0] == '"' {
					var s string
					if json.Unmarshal(sha, &s) == nil {
						r.Commit = ptr(pyPrefix(s, 7))
					}
				}
			}
		}
	}
	md := pyObject(ev.turnMD)
	var approval, sandboxPolicy *string
	if v := md["approval_policy"]; len(v) > 0 && v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			approval = &s
		}
	}
	if v := md["sandbox_policy"]; len(v) > 0 && v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			sandboxPolicy = &s
		}
	}
	if sandbox == nil || *sandbox == "" {
		sandbox = sandboxPolicy
	}
	if cwd != nil || approval != nil || sandbox != nil {
		r.FactsCwd, r.FactsApproval, r.FactsSandbox, r.HasFacts = cwd, approval, sandbox, true
	}
	if limits != nil {
		r.LimitsJSON, r.LimitsNS = codexLimits(limits.provider, limits.ts), ptr(limits.ts)
	}
}

// pyFirstField is a JSON object's first key (Python's next(iter(d))) and its value
// (the last of a repeated key); false unless it's an object with a field.
func pyFirstField(v json.RawMessage) (string, json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(v))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') || !dec.More() {
		return "", nil, false
	}
	tok, err := dec.Token()
	if err != nil {
		return "", nil, false
	}
	first, _ := tok.(string)
	return first, pyObject(string(v))[first], true
}

// pyBasename is path.rstrip("/").rsplit("/", 1)[-1].
func pyBasename(p string) string {
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

// pyPrefix is s[:n] (code points).
func pyPrefix(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// --- Codex limits (agents.codex_limits) ---------------------------------------------

type limitWindowJSON struct {
	Name     string   `json:"name"`
	UsedPct  *float64 `json:"usedPct"`
	ResetsAt *string  `json:"resetsAt"`
}

type agentLimitsJSON struct {
	Agent   string            `json:"agent"`
	Plan    *string           `json:"plan"`
	Windows []limitWindowJSON `json:"windows"`
	Credits *string           `json:"credits"`
	AsOf    string            `json:"asOf"`
}

// codexLimits is agents.codex_limits of one response's x-codex-* headers, as of
// datetime.fromtimestamp(ts / 1e9): nil when it sent none.
func codexLimits(providerJSON string, ts int64) *string {
	h := map[string]string{}
	for k, v := range pyObject(providerJSON) {
		if s, ok := pyJSONString(v); ok {
			h[k] = s
		}
	}
	get := func(k string) (string, bool) { v, ok := h[k]; return v, ok }
	var windows []limitWindowJSON
	for _, w := range []string{"primary", "secondary"} {
		if _, ok := get("x-codex-" + w + "-used-percent"); !ok {
			continue
		}
		minutes, hasMinutes := get("x-codex-" + w + "-window-minutes")
		if hasMinutes && (minutes == "0" || minutes == "") {
			continue
		}
		used, _ := get("x-codex-" + w + "-used-percent")
		reset, _ := get("x-codex-" + w + "-reset-at")
		windows = append(windows, limitWindowJSON{Name: windowName(minutes, w), UsedPct: pyFloat(used), ResetsAt: pyEpoch(reset)})
	}
	plan, hasPlan := get("x-codex-plan-type")
	if len(windows) == 0 && plan == "" {
		return nil
	}
	l := agentLimitsJSON{Agent: "codex", Windows: windows, AsOf: pyFromTimestamp(ts)}
	if l.Windows == nil {
		l.Windows = []limitWindowJSON{}
	}
	if hasPlan {
		l.Plan = ptr(plan)
	}
	if u, _ := get("x-codex-credits-unlimited"); u == "True" || u == "true" {
		l.Credits = ptr("unlimited")
	} else if b, _ := get("x-codex-credits-balance"); b != "" {
		l.Credits = ptr(credits(b))
	}
	return ptr(marshalJSON(l))
}

// pyJSONString is a header value as the API reads it: only strings count (the
// headers are strings).
func pyJSONString(v json.RawMessage) (string, bool) {
	if len(v) == 0 || v[0] != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(v, &s) == nil
}

// windowName is agents._window_name.
func windowName(minutes, fallback string) string {
	switch minutes {
	case "300":
		return "5h"
	case "10080":
		return "week"
	}
	n, ok := pyParseInt(minutes)
	if !ok {
		return fallback
	}
	if pyMod(n, 1440) != 0 {
		return strconv.FormatInt(pyDiv(n, 60), 10) + "h"
	}
	return strconv.FormatInt(pyDiv(n, 1440), 10) + "d"
}

func pyDiv(a, b int64) int64 { return int64(math.Floor(float64(a) / float64(b))) }
func pyMod(a, b int64) int64 { return a - pyDiv(a, b)*b }

// pyParseInt is Python's int(s) for a decimal string; false where it raises.
func pyParseInt(s string) (int64, bool) {
	s = strings.TrimFunc(s, pyIsSpace)
	s = strings.TrimPrefix(s, "+")
	if strings.Contains(s, "_") {
		if strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
			return 0, false
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// pyFloat is agents._pct: float(v), None for "" or what float() refuses.
func pyFloat(s string) *float64 {
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimFunc(s, pyIsSpace), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

// pyEpoch is agents._ts: datetime.fromtimestamp(int(v)) as the API writes it.
func pyEpoch(s string) *string {
	if s == "" {
		return nil
	}
	n, ok := pyParseInt(s)
	if !ok {
		return nil
	}
	return ptr(time.Unix(n, 0).UTC().Format("2006-01-02T15:04:05Z07:00"))
}

// pyFromTimestamp is datetime.fromtimestamp(ts / 1e9, UTC): the float seconds,
// rounded half to even to the microsecond.
func pyFromTimestamp(ns int64) string {
	t := float64(ns) / 1e9
	sec := math.Floor(t)
	us := math.RoundToEven((t - sec) * 1e6)
	if us >= 1e6 {
		sec, us = sec+1, us-1e6
	}
	return time.Unix(int64(sec), int64(us)*1000).UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}

// credits is agents._credits: whole credits with separators ("50,652").
func credits(v string) string {
	f, err := strconv.ParseFloat(strings.TrimFunc(v, pyIsSpace), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return v
	}
	n, _ := new(big.Float).SetFloat64(math.RoundToEven(f)).Int(nil)
	s := n.String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
