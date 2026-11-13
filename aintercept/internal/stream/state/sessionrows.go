package state

import (
	"encoding/gob"
	"encoding/json"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Session rows (process sessionrows.go), per session key: one stored row of
// every value that's the session's own, kept as incremental aggregates over its
// calls and events, so a record changes a few counters and picks, never a
// rebuild. Lists are rows of their own (prompt ids, devices, call accounts,
// missed reports: one row each, so a record adds or drops one); what spans
// sessions is stated by the session it comes from and joined by the query:
//   - session_edges: a subagent or review thread's edge to its parent, with its
//     first time (the parent's children_json);
//   - session_title_calls: a title thread's title calls (another thread's title,
//     assigned per account in time order by the query: §4.5, sessionrows_test.go
//     sessionRowsOf and api/src/aintercept_api/sql/session_rows.sql).
const (
	TableSessionRows         = "session_rows"
	TableSessionPromptIDs    = "session_prompt_ids"
	TableSessionDevices      = "session_devices"
	TableSessionCallAccounts = "session_call_accounts"
	TableSessionMissed       = "session_missed"
	TableSessionEdges        = "session_edges"
	TableSessionTitleCalls   = "session_title_calls"
)

// The cases a record takes in the session rows.
const (
	CaseSesCallVisible   = "ses.call.visible"   // a visible model call counted (or its in-flight row replaced)
	CaseSesCallUnvisible = "ses.call.unvisible" // a call in flight whose ending the session doesn't count
	CaseSesCallHidden    = "ses.call.hidden"    // a coding agent's hidden call (prewarm, title)
	CaseSesCallTitle     = "ses.call.title"     // … a title call with an answer
	CaseSesCallCodex     = "ses.call.codex"     // a Codex agent call: workspace, sandbox, limits
	CaseSesCallOther     = "ses.call.other"     // a call no session row reads
	CaseSesTitleThread   = "ses.title.thread"   // a title call of a thread with no visible call: names another
	CaseSesTitleDrop     = "ses.title.drop"     // a title thread gained a visible call: its title calls withdrawn
	CaseSesOrigin        = "ses.origin"         // an ended model call: the session's first time and lineage
	CaseSesEdge          = "ses.edge"           // a subagent or review thread's edge to its parent, made or changed
	CaseSesEvent         = "ses.event"          // a Claude Code event of the session
	CaseSesEventQuery    = "ses.event.query"    // … a tengu_api_query with a permission mode
	CaseSesEventPrompt   = "ses.event.prompt"   // … an input prompt
	CaseSesEventFriction = "ses.event.friction" // … a negative prompt, a cancel, a destructive command
	CaseSesEventReport   = "ses.event.report"   // … a report (tengu_api_success)
	CaseSesEventTurn     = "ses.event.turn"     // a Codex turn event
	CaseSesReportMissed  = "ses.report.missed"  // a report whose call isn't captured (made, or a call withdrawn)
	CaseSesReportMatched = "ses.report.matched" // a missed report's call captured
)

var sessionCaseList = []string{CaseSesCallVisible, CaseSesCallUnvisible, CaseSesCallHidden, CaseSesCallTitle, CaseSesCallCodex,
	CaseSesCallOther, CaseSesTitleThread, CaseSesTitleDrop, CaseSesOrigin, CaseSesEdge, CaseSesEvent, CaseSesEventQuery,
	CaseSesEventPrompt, CaseSesEventFriction, CaseSesEventReport, CaseSesEventTurn, CaseSesReportMissed, CaseSesReportMatched}

func init() {
	AllCases = append(AllCases, sessionCaseList...)
	RowTypes = append(RowTypes, SessionRowK{}, SessionPromptID{}, SessionDevice{}, SessionCallAccount{}, SessionMissed{},
		SessionEdge{}, SessionTitleCall{})
	for _, v := range []any{SessionRowK{}, SessionPromptID{}, SessionDevice{}, SessionCallAccount{}, SessionMissed{}, SessionEdge{}, SessionTitleCall{}} {
		gob.Register(v)
	}
}

// SessionRowK is a session's stored row: process.SessionRow without its lists
// (rows of their own), children_json (edges) and title labels (the query's), and
// with the session's own title.
type SessionRowK struct {
	SessionID       string  `parquet:"session_id"`
	StartNS         int64   `parquet:"start_ns"`
	LastNS          int64   `parquet:"last_ns"`
	Product         string  `parquet:"product"`
	Agent           string  `parquet:"agent"`
	Client          *string `parquet:"client,optional"`
	ClientVersion   *string `parquet:"client_version,optional"`
	ClientVersionNS *int64  `parquet:"client_version_ns,optional"`

	OriginKind      *string `parquet:"origin_kind,optional"`
	OriginSessionID *string `parquet:"origin_session_id,optional"`
	OriginJSON      *string `parquet:"origin_json,optional"`
	HiddenCalls     int64   `parquet:"hidden_calls"`
	OwnTitle        *string `parquet:"own_title,optional"`
	Repo            *string `parquet:"repo,optional"`
	Commit          *string `parquet:"commit,optional"`

	Prompts            *int64   `parquet:"prompts,optional"`
	CapturedFromPrompt *int64   `parquet:"captured_from_prompt,optional"`
	ModelCalls         int64    `parquet:"model_calls"`
	Events             *int64   `parquet:"events,optional"`
	Frustrated         int64    `parquet:"frustrated"`
	Interrupted        int64    `parquet:"interrupted"`
	Destructive        int64    `parquet:"destructive"`
	BypassPct          *float64 `parquet:"bypass_pct,optional"`
	CovReported        *int64   `parquet:"cov_reported,optional"`
	CovCaptured        int64    `parquet:"cov_captured"`
	CovNotCaptured     int64    `parquet:"cov_not_captured"`
	FieldsMissing      bool     `parquet:"fields_missing"`

	PersonID         *string `parquet:"person_id,optional"`
	DeviceID         *string `parquet:"device_id,optional"`
	DevicePlatform   *string `parquet:"device_platform,optional"`
	DeviceTerminal   *string `parquet:"device_terminal,optional"`
	DeviceShell      *string `parquet:"device_shell,optional"`
	Networks         *int64  `parquet:"networks,optional"`
	Effort           *string `parquet:"effort,optional"`
	TokensInput      *int64  `parquet:"tokens_input,optional"`
	TokensCacheRead  *int64  `parquet:"tokens_cache_read,optional"`
	TokensCacheWrite *int64  `parquet:"tokens_cache_write,optional"`
	TokensOutput     *int64  `parquet:"tokens_output,optional"`
	TokensAccuracy   string  `parquet:"tokens_accuracy"`
	ToolCalls        *int64  `parquet:"tool_calls,optional"`
	HasFacts         bool    `parquet:"has_facts"`
	FactsCwd         *string `parquet:"facts_cwd,optional"`
	FactsApproval    *string `parquet:"facts_approval,optional"`
	FactsSandbox     *string `parquet:"facts_sandbox,optional"`
	LimitsJSON       *string `parquet:"limits_json,optional"`
	LimitsNS         *int64  `parquet:"limits_ns,optional"`

	OutputsKnown int64    `parquet:"outputs_known"`
	Queries      int64    `parquet:"queries"`
	Bypassed     int64    `parquet:"bypassed"`
	CLIVersion   *string  `parquet:"cli_version,optional"`
	CLIVersionNS *int64   `parquet:"cli_version_ns,optional"`
	RLStatus     *string  `parquet:"rl_status,optional"`
	RLUtil       *float64 `parquet:"rl_util,optional"`
	RLOverage    *string  `parquet:"rl_overage,optional"`
	RLNS         *int64   `parquet:"rl_ns,optional"`
	Sandbox      *string  `parquet:"sandbox,optional"`
	SandboxNS    *int64   `parquet:"sandbox_ns,optional"`
	CustomCA     bool     `parquet:"custom_ca"`
}

// row is the stored row as a process.SessionRow, before the query adds lists,
// children and titles.
func (k SessionRowK) row() process.SessionRow {
	return process.SessionRow{
		SessionID: k.SessionID, StartNS: k.StartNS, LastNS: k.LastNS, Product: k.Product, Agent: k.Agent,
		Client: k.Client, ClientVersion: k.ClientVersion, OriginKind: k.OriginKind, OriginSessionID: k.OriginSessionID,
		OriginJSON: k.OriginJSON, HiddenCalls: k.HiddenCalls, Title: k.OwnTitle, TitleAlone: k.OwnTitle, Repo: k.Repo,
		Commit: k.Commit, Prompts: k.Prompts, CapturedFromPrompt: k.CapturedFromPrompt, ModelCalls: k.ModelCalls,
		Events: k.Events, Frustrated: k.Frustrated, Interrupted: k.Interrupted, Destructive: k.Destructive,
		BypassPct: k.BypassPct, CovReported: k.CovReported, CovCaptured: k.CovCaptured, CovNotCaptured: k.CovNotCaptured,
		FieldsMissing: k.FieldsMissing, PersonID: k.PersonID, DeviceID: k.DeviceID, DevicePlatform: k.DevicePlatform,
		DeviceTerminal: k.DeviceTerminal, DeviceShell: k.DeviceShell, Networks: k.Networks, Effort: k.Effort,
		TokensInput: k.TokensInput, TokensCacheRead: k.TokensCacheRead, TokensCacheWrite: k.TokensCacheWrite,
		TokensOutput: k.TokensOutput, TokensAccuracy: k.TokensAccuracy, ToolCalls: k.ToolCalls, HasFacts: k.HasFacts,
		FactsCwd: k.FactsCwd, FactsApproval: k.FactsApproval, FactsSandbox: k.FactsSandbox, LimitsJSON: k.LimitsJSON,
		LimitsNS: k.LimitsNS, OutputsKnown: k.OutputsKnown, Queries: k.Queries, Bypassed: k.Bypassed,
		CLIVersion: k.CLIVersion, CLIVersionNS: k.CLIVersionNS, ClientVersionNS: k.ClientVersionNS, RLStatus: k.RLStatus,
		RLUtil: k.RLUtil, RLOverage: k.RLOverage, RLNS: k.RLNS, Sandbox: k.Sandbox, SandboxNS: k.SandboxNS, CustomCA: k.CustomCA,
	}
}

type SessionPromptID struct {
	SessionID string `parquet:"session_id"`
	PromptID  string `parquet:"prompt_id"`
}

type SessionDevice struct {
	SessionID string `parquet:"session_id"`
	DeviceID  string `parquet:"device_id"`
}

type SessionCallAccount struct {
	SessionID string `parquet:"session_id"`
	AccountID string `parquet:"account_id"`
}

// SessionMissed is a report (tengu_api_success) whose call isn't captured.
type SessionMissed struct {
	SessionID string `parquet:"session_id"`
	TSNS      int64  `parquet:"ts_ns"`
	Key       string `parquet:"event_key"`
}

// SessionEdge is a subagent or review thread's edge to its parent.
type SessionEdge struct {
	ParentSessionID string `parquet:"parent_session_id"`
	ChildSessionID  string `parquet:"child_session_id"`
	Kind            string `parquet:"kind"`
	ChildFirstNS    int64  `parquet:"child_first_ns"`
}

// SessionTitleCall is a title thread's title call: it names another thread.
type SessionTitleCall struct {
	SessionID string `parquet:"session_id"`
	TSNS      int64  `parquet:"ts_ns"`
	CallID    string `parquet:"call_id"`
	AccountID string `parquet:"account_id"`
	Title     string `parquet:"title"`
}

// --- the aggregates ---------------------------------------------------------------

// pk orders a pick's candidates: by time, a second time, then two strings.
type pk struct {
	ts, ts2 int64
	a, b    string
}

func lessPK(x, y pk) bool {
	switch {
	case x.ts != y.ts:
		return x.ts < y.ts
	case x.ts2 != y.ts2:
		return x.ts2 < y.ts2
	case x.a != y.a:
		return x.a < y.a
	}
	return x.b < y.b
}

// lessPKLastID is by time, then id descending: the newest, the first id at a time.
func lessPKLastID(x, y pk) bool {
	if x.ts != y.ts {
		return x.ts < y.ts
	}
	return x.b > y.b
}

// picks is a pick's candidates: first and last are its least and greatest.
type picks = omap[pk, string]

func newPicks(less func(a, b pk) bool) *picks { return newOmap[pk, string](less) }

func firstOf(m *picks) (pk, string, bool) {
	if m.Len() == 0 {
		return pk{}, "", false
	}
	e := m.chunks[0].e[0]
	return e.k, e.v, true
}

func lastOf(m *picks) (pk, string, bool) {
	if m.Len() == 0 {
		return pk{}, "", false
	}
	c := m.chunks[len(m.chunks)-1].e
	e := c[len(c)-1]
	return e.k, e.v, true
}

// counts is a multiset: the distinct values are rows of their own.
type counts map[string]int

// sesEvents is what the session's events give its row: all folded in, never
// taken back.
type sesEvents struct {
	Events, Queries, Bypass, Negative, Cancels, Destroy int64

	Account   string
	AccountNS int64
	Device    string
	DeviceNS  int64
	Env       [3]Pick
	Sandbox   Pick
	CustomCA  bool
	MinIdx    *int64
	Turn      Pick // the newest codex_turn_event's metadata
	Prompts   map[string]bool
	Devices   map[string]bool
}

type sesReport struct {
	TS        int64
	Rid, Crid string
}

// sesRows is a session key's session-row state.
type sesRows struct {
	calls   map[string]process.SessionCall // a coding agent's calls, by call id: in flight or ended
	ev      sesEvents
	reports map[string]sesReport // by event key
	origin  process.Session      // the sessions snapshot's row: first time and lineage
	isOrig  bool

	// Rebuilt from the calls and reports.
	vis, withOut, toolsAny, tools, in, read, write, out, known, hidden int64
	product, client, version, person, device, effort, cli, rl, title   *picks
	ext, sbx, prov                                                     *picks
	prompts, devices, accounts, ips, rids, crids                       counts
	byRid, byCrid                                                      map[string]map[string]bool
	missed                                                             map[string]bool
	titles                                                             map[string]process.SessionCall // hidden title calls with an answer
}

func newSesRows() *sesRows {
	x := &sesRows{calls: map[string]process.SessionCall{}, reports: map[string]sesReport{}}
	x.ev.Prompts, x.ev.Devices = map[string]bool{}, map[string]bool{}
	x.reset()
	return x
}

func (x *sesRows) reset() {
	x.vis, x.withOut, x.toolsAny, x.tools, x.in, x.read, x.write, x.out, x.known, x.hidden = 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
	for _, p := range []**picks{&x.product, &x.client, &x.version, &x.person, &x.device, &x.cli, &x.rl, &x.title, &x.ext, &x.sbx, &x.prov} {
		*p = newPicks(lessPK)
	}
	x.effort = newPicks(lessPKLastID)
	x.prompts, x.devices, x.accounts, x.ips, x.rids, x.crids = counts{}, counts{}, counts{}, counts{}, counts{}, counts{}
	x.byRid, x.byCrid, x.missed = map[string]map[string]bool{}, map[string]map[string]bool{}, map[string]bool{}
	x.titles = map[string]process.SessionCall{}
}

// sesChange is what one record changed in the list rows.
type sesChange struct {
	prompts, devices, accounts map[string]int // +1 made, -1 gone
	rids, crids                []string       // ids whose count went to or from zero
	reports                    []string       // reports made
}

func (c *sesChange) mark(m *map[string]int, k string, v int) {
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[k] += v
}

func (x *sesRows) count(m counts, k string, sign int, onEdge func()) {
	if k == "" {
		return
	}
	before := m[k]
	m[k] += sign
	if m[k] <= 0 {
		delete(m, k)
	}
	if (before == 0) != (m[k] == 0) && onEdge != nil {
		onEdge()
	}
}

// contribute adds (sign 1) or takes back (-1) one call's part in the row.
func (x *sesRows) contribute(c process.SessionCall, sign int, ch *sesChange) {
	s := int64(sign)
	set := func(m *picks, k pk, v string) {
		if sign > 0 {
			m.Set(k, v)
		} else {
			m.Delete(k)
		}
	}
	if c.Agent && c.Hidden != "" {
		x.hidden += s
		if c.Hidden == "title" && c.Text != nil && *c.Text != "" {
			set(x.title, pk{ts: c.TS, b: c.ID}, process.StripTitle(*c.Text))
			if sign > 0 {
				x.titles[c.ID] = c
			} else {
				delete(x.titles, c.ID)
			}
		}
	}
	if c.Agent && c.Product == process.ProductCodex {
		if c.Ext != "" {
			set(x.ext, pk{ts: c.TS, a: c.Ext, b: c.ID}, c.Ext)
		}
		if c.Sandbox != nil {
			set(x.sbx, pk{ts: c.TS, a: *c.Sandbox, b: c.ID}, *c.Sandbox)
		}
		if c.Provider != "" {
			set(x.prov, pk{ts: c.TS, a: c.ID}, c.Provider)
		}
	}
	if !c.Visible {
		return
	}
	x.vis += s
	x.in, x.read, x.write, x.out = x.in+s*c.Input, x.read+s*c.CacheRead, x.write+s*c.CacheWrite, x.out+s*c.Output
	if c.Output != 0 {
		x.withOut += s
	}
	if c.HasTools {
		x.toolsAny += s
		x.tools += s * c.ToolCalls
	}
	if c.Person != "" || c.Device != "" {
		x.known += s
	}
	if c.Product != "" {
		set(x.product, pk{ts: c.TS, b: c.ID}, c.Product)
	}
	if c.Client != "" {
		set(x.client, pk{ts: c.TS, b: c.ID}, c.Client)
	}
	if c.ClientVersion != "" {
		set(x.version, pk{ts: c.TS, b: c.ID}, c.ClientVersion)
	}
	if c.Person != "" {
		set(x.person, pk{ts: c.TS, a: c.Person, b: c.ID}, c.Person)
	}
	if c.Device != "" {
		set(x.device, pk{ts: c.TS, a: c.Device, b: c.ID}, c.Device)
	}
	if c.Effort != "" {
		set(x.effort, pk{ts: c.TS, b: c.ID}, c.Effort)
	}
	if c.CLI != nil {
		set(x.cli, pk{ts: c.TS, a: *c.CLI, b: c.ID}, *c.CLI)
	}
	if c.RL != nil {
		v, _ := json.Marshal(c.RL)
		set(x.rl, pk{ts: c.RL.At, ts2: c.TS, a: c.ID}, string(v))
	}
	x.count(x.prompts, c.Prompt, sign, func() { ch.mark(&ch.prompts, c.Prompt, sign) })
	x.count(x.devices, c.Device, sign, func() { ch.mark(&ch.devices, c.Device, sign) })
	x.count(x.accounts, c.Person, sign, func() { ch.mark(&ch.accounts, c.Person, sign) })
	x.count(x.ips, c.IP, sign, nil)
	x.count(x.rids, c.ReqID, sign, func() { ch.rids = append(ch.rids, c.ReqID) })
	x.count(x.crids, c.ClientReqID, sign, func() { ch.crids = append(ch.crids, c.ClientReqID) })
}

// isMissed: a report whose call isn't among the session's visible calls.
func (x *sesRows) isMissed(r sesReport) bool {
	return !(r.Rid != "" && x.rids[r.Rid] > 0) && !(r.Crid != "" && x.crids[r.Crid] > 0)
}

// --- applying a record --------------------------------------------------------------

func (s *KeyState) sessionRows(r records.Record, d *Delta) {
	x, sid := s.sr, sessionOfKey(s.Key)
	if sid == "" {
		return
	}
	var ch sesChange
	hadVisible := x.vis > 0
	switch r.Kind {
	case records.KindCallEnd, records.KindWSConnection, records.KindCallStart:
		c := r.Call
		if c == nil {
			return
		}
		var f process.SessionCall
		if r.Kind == records.KindCallStart {
			if s.ses.ended[c.CallID] || strings.Contains(c.CallID, "#") {
				return
			}
			f = process.SessionPendingOf(pendingFor(c))
		} else {
			f = process.SessionCallOf(*c)
			if process.SessionLineage(&x.origin, *c) {
				x.isOrig = true
				d.Cases = append(d.Cases, CaseSesOrigin)
			}
		}
		old, had := x.calls[c.CallID]
		if had {
			x.contribute(old, -1, &ch)
			delete(x.calls, c.CallID)
		}
		if !f.Agent {
			if had && old.Visible {
				d.Cases = append(d.Cases, CaseSesCallUnvisible)
			} else if !had {
				d.Cases = append(d.Cases, CaseSesCallOther)
			}
			break
		}
		x.calls[c.CallID] = f
		x.contribute(f, 1, &ch)
		switch {
		case f.Visible:
			d.Cases = append(d.Cases, CaseSesCallVisible)
		default:
			if had && old.Visible {
				d.Cases = append(d.Cases, CaseSesCallUnvisible)
			}
			d.Cases = append(d.Cases, CaseSesCallHidden)
			if f.Hidden == "title" && f.Text != nil && *f.Text != "" {
				d.Cases = append(d.Cases, CaseSesCallTitle)
			}
		}
		if f.Product == process.ProductCodex {
			d.Cases = append(d.Cases, CaseSesCallCodex)
		}
	case records.KindClientEvent:
		e := r.Event
		if e == nil {
			return
		}
		if (e.Source == canon.ProductClaudeCode || e.Source == canon.ProductCodex) && e.SessionID != "" {
			x.origin.FirstNS = minNZ(x.origin.FirstNS, e.NS)
			x.isOrig = true
		}
		f := process.SessionEventOf(*e)
		if f.Turn {
			if x.ev.Turn.offer(f.NS, f.TurnMD) {
				d.Cases = append(d.Cases, CaseSesEventTurn)
			}
		}
		if f.CC {
			x.event(f, d, &ch)
		}
	default:
		return
	}
	s.sessionLists(sid, &ch, d)
	if hadVisible != (x.vis > 0) || r.Kind != records.KindClientEvent {
		s.titleCalls(sid, hadVisible, d)
	}
	s.sessionEdge(sid, d)
	s.sessionRow(sid, d)
}

func (x *sesRows) event(f process.SessionEvent, d *Delta, ch *sesChange) {
	ev := &x.ev
	ev.Events++
	d.Cases = append(d.Cases, CaseSesEvent)
	if f.Person != "" && (ev.Account == "" || f.NS < ev.AccountNS || f.NS == ev.AccountNS && f.Person < ev.Account) {
		ev.Account, ev.AccountNS = f.Person, f.NS
	}
	if f.Device != "" {
		if ev.Device == "" || f.NS < ev.DeviceNS || f.NS == ev.DeviceNS && f.Device < ev.Device {
			ev.Device, ev.DeviceNS = f.Device, f.NS
		}
		if !ev.Devices[f.Device] {
			ev.Devices[f.Device] = true
			x.count(x.devices, f.Device, 1, func() { ch.mark(&ch.devices, f.Device, 1) })
		}
	}
	for i := range f.Env {
		if f.EnvOK[i] {
			ev.Env[i].offer(f.NS, f.Env[i])
		}
	}
	if f.Query {
		ev.Queries++
		if f.Bypass {
			ev.Bypass++
		}
		d.Cases = append(d.Cases, CaseSesEventQuery)
	}
	if f.Negative || f.Cancel || f.Destroy {
		d.Cases = append(d.Cases, CaseSesEventFriction)
		if f.Negative {
			ev.Negative++
		}
		if f.Cancel {
			ev.Cancels++
		}
		if f.Destroy {
			ev.Destroy++
		}
	}
	if f.Sandbox != nil {
		ev.Sandbox.offer(f.NS, *f.Sandbox)
	}
	ev.CustomCA = ev.CustomCA || f.CustomCA
	if f.Prompt {
		d.Cases = append(d.Cases, CaseSesEventPrompt)
		if !ev.Prompts[f.PromptKey] {
			ev.Prompts[f.PromptKey] = true
			x.count(x.prompts, f.PromptKey, 1, func() { ch.mark(&ch.prompts, f.PromptKey, 1) })
		}
		if f.PromptIndex != nil && (ev.MinIdx == nil || *f.PromptIndex < *ev.MinIdx) {
			v := *f.PromptIndex
			ev.MinIdx = &v
		}
	}
	if f.Report {
		d.Cases = append(d.Cases, CaseSesEventReport)
		rep := sesReport{TS: f.NS, Rid: f.ReqID, Crid: f.ClientReqID}
		x.reports[f.Key] = rep
		x.indexReport(f.Key, rep)
		ch.reports = append(ch.reports, f.Key)
	}
}

func (x *sesRows) indexReport(key string, rep sesReport) {
	if rep.Rid != "" {
		if x.byRid[rep.Rid] == nil {
			x.byRid[rep.Rid] = map[string]bool{}
		}
		x.byRid[rep.Rid][key] = true
	}
	if rep.Crid != "" {
		if x.byCrid[rep.Crid] == nil {
			x.byCrid[rep.Crid] = map[string]bool{}
		}
		x.byCrid[rep.Crid][key] = true
	}
}

// sessionLists writes the list rows a record changed, and the missed reports.
func (s *KeyState) sessionLists(sid string, ch *sesChange, d *Delta) {
	x := s.sr
	for _, l := range []struct {
		m     map[string]int
		table string
		row   func(string) any
	}{
		{ch.prompts, TableSessionPromptIDs, func(v string) any { return SessionPromptID{sid, v} }},
		{ch.devices, TableSessionDevices, func(v string) any { return SessionDevice{sid, v} }},
		{ch.accounts, TableSessionCallAccounts, func(v string) any { return SessionCallAccount{sid, v} }},
	} {
		for v, n := range l.m {
			switch {
			case n > 0:
				s.rows.put(d, l.table, v, l.row(v))
			case n < 0:
				s.rows.drop(d, l.table, v)
			}
		}
	}
	// Reports whose match may have changed: a new report, or a call id that came or went.
	check := map[string]bool{}
	for _, k := range ch.reports {
		check[k] = true
	}
	for _, id := range ch.rids {
		for k := range x.byRid[id] {
			check[k] = true
		}
	}
	for _, id := range ch.crids {
		for k := range x.byCrid[id] {
			check[k] = true
		}
	}
	for k := range check {
		rep := x.reports[k]
		now, was := x.isMissed(rep), x.missed[k]
		switch {
		case now && !was:
			x.missed[k] = true
			s.rows.put(d, TableSessionMissed, k, SessionMissed{sid, rep.TS, k})
			d.Cases = append(d.Cases, CaseSesReportMissed)
		case !now && was:
			delete(x.missed, k)
			s.rows.drop(d, TableSessionMissed, k)
			d.Cases = append(d.Cases, CaseSesReportMatched)
		}
	}
}

// titleCalls writes a title thread's title calls: while the session has no
// visible call, each of its title calls with an answer names another thread.
func (s *KeyState) titleCalls(sid string, hadVisible bool, d *Delta) {
	x := s.sr
	visible := x.vis > 0
	want := map[string]SessionTitleCall{}
	if !visible {
		for id, c := range x.titles {
			want[id] = SessionTitleCall{SessionID: sid, TSNS: c.TS, CallID: c.ID, AccountID: c.Person, Title: process.StripTitle(*c.Text)}
		}
	}
	dropped := false
	for id := range s.rows[TableSessionTitleCalls] {
		if _, ok := want[id]; !ok {
			s.rows.drop(d, TableSessionTitleCalls, id)
			dropped = true
		}
	}
	if dropped && visible && !hadVisible {
		d.Cases = append(d.Cases, CaseSesTitleDrop)
	}
	for id, row := range want {
		if cur, ok := s.rows[TableSessionTitleCalls][id]; !ok || cur != any(row) {
			d.Cases = append(d.Cases, CaseSesTitleThread)
		}
		s.rows.put(d, TableSessionTitleCalls, id, row)
	}
}

// sessionEdge writes a subagent or review thread's edge to its parent.
func (s *KeyState) sessionEdge(sid string, d *Delta) {
	o := s.sr.origin
	if (o.OriginKind == "subagent" || o.OriginKind == "review") && o.OriginSessionID != "" {
		e := SessionEdge{ParentSessionID: o.OriginSessionID, ChildSessionID: sid, Kind: o.OriginKind, ChildFirstNS: o.FirstNS}
		if cur, ok := s.rows[TableSessionEdges]["edge"]; !ok || cur != any(e) {
			d.Cases = append(d.Cases, CaseSesEdge)
		}
		s.rows.put(d, TableSessionEdges, "edge", e)
		return
	}
	s.rows.drop(d, TableSessionEdges, "edge")
}

// sessionRow writes the session's row, or drops it when the session has neither
// a visible call nor a Claude Code event.
func (s *KeyState) sessionRow(sid string, d *Delta) {
	x := s.sr
	if x.vis == 0 && x.ev.Events == 0 {
		s.rows.drop(d, TableSessionRows, "row")
		return
	}
	sp := s.ses.spanNow()
	r := SessionRowK{SessionID: sid, StartNS: sp.first, LastNS: sp.last, ModelCalls: x.vis, Product: process.ProductClaudeCode}
	if _, v, ok := firstOf(x.product); ok {
		r.Product = v
	}
	if x.vis == 0 {
		r.Product = ""
	}
	r.Agent = process.AgentOf(r.Product)
	if _, v, ok := lastOf(x.client); ok {
		r.Client = &v
	}
	if k, v, ok := lastOf(x.version); ok {
		ts := k.ts
		r.ClientVersion, r.ClientVersionNS = &v, &ts
	}
	if x.isOrig {
		o := x.origin
		r.OriginKind, r.OriginSessionID, r.OriginJSON = process.SessionOrigin(&o)
	}
	r.HiddenCalls = x.hidden
	if _, v, ok := lastOf(x.title); ok {
		r.OwnTitle = &v
	}
	ev := &x.ev
	if n := int64(len(x.prompts)); n > 0 {
		r.Prompts = &n
	}
	if ev.MinIdx != nil && *ev.MinIdx > 1 {
		v := *ev.MinIdx
		r.CapturedFromPrompt = &v
	}
	if ev.Events > 0 {
		n := ev.Events
		r.Events = &n
	}
	r.Frustrated, r.Interrupted, r.Destructive = ev.Negative, ev.Cancels, ev.Destroy
	if ev.Queries > 0 {
		v := process.Round1(float64(100*ev.Bypass) / float64(ev.Queries))
		r.BypassPct = &v
	}
	if ev.Events == 0 {
		r.CovCaptured = x.vis
	} else {
		n, m := int64(len(x.reports)), int64(len(x.missed))
		r.CovReported, r.CovCaptured, r.CovNotCaptured = &n, n-m, m
	}
	r.FieldsMissing = x.vis > 0 && x.known == 0
	switch {
	case ev.Account != "":
		v := ev.Account
		r.PersonID = &v
	default:
		if _, v, ok := firstOf(x.person); ok {
			r.PersonID = &v
		}
	}
	dev := ev.Device
	if dev == "" {
		if _, v, ok := firstOf(x.device); ok {
			dev = v
		}
	}
	if dev != "" {
		r.DeviceID = &dev
		r.DevicePlatform, r.DeviceTerminal, r.DeviceShell = ev.Env[0].ptr(), ev.Env[1].ptr(), ev.Env[2].ptr()
	}
	if n := int64(len(x.ips)); n > 0 {
		r.Networks = &n
	}
	if _, v, ok := lastOf(x.effort); ok {
		r.Effort = &v
	}
	r.TokensInput, r.TokensCacheRead, r.TokensCacheWrite, r.TokensOutput = nzi(x.in), nzi(x.read), nzi(x.write), nzi(x.out)
	r.OutputsKnown = x.withOut
	switch {
	case x.vis > 0 && x.withOut == x.vis:
		r.TokensAccuracy = "exact"
	case x.withOut == 0:
		r.TokensAccuracy = "missing"
	default:
		r.TokensAccuracy = "estimated"
	}
	if x.toolsAny > 0 {
		n := x.tools
		r.ToolCalls = &n
	}
	r.Queries, r.Bypassed = ev.Queries, ev.Bypass
	if k, v, ok := lastOf(x.cli); ok {
		ts := k.ts
		r.CLIVersion, r.CLIVersionNS = &v, &ts
	}
	if _, v, ok := lastOf(x.rl); ok {
		var rl process.SessionRL
		json.Unmarshal([]byte(v), &rl)
		at := rl.At
		r.RLStatus, r.RLUtil, r.RLOverage, r.RLNS = &rl.Status, rl.Util, rl.Overage, &at
	}
	if ev.Sandbox.OK {
		v, ts := ev.Sandbox.V, ev.Sandbox.TS
		r.Sandbox, r.SandboxNS = &v, &ts
	}
	r.CustomCA = ev.CustomCA
	if r.Product == process.ProductCodex {
		var ext string
		var sbx *string
		if _, v, ok := lastOf(x.ext); ok {
			ext = v
		}
		if _, v, ok := lastOf(x.sbx); ok {
			sbx = &v
		}
		turn := ""
		if ev.Turn.OK {
			turn = ev.Turn.V
		}
		r.Repo, r.Commit, r.FactsCwd, r.FactsApproval, r.FactsSandbox, r.HasFacts = process.CodexSessionFacts(ext, sbx, turn)
		if k, v, ok := lastOf(x.prov); ok {
			ts := k.ts
			r.LimitsJSON, r.LimitsNS = process.CodexLimits(v, ts), &ts
		}
	}
	s.rows.put(d, TableSessionRows, "row", r)
}

func (p Pick) ptr() *string {
	if !p.OK {
		return nil
	}
	v := p.V
	return &v
}

func nzi(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func minNZ(a, b int64) int64 {
	if a == 0 || b != 0 && b < a {
		return b
	}
	return a
}

// sessionOfKey is a session key's session id ("" for any other key).
func sessionOfKey(key string) string {
	if strings.HasPrefix(key, "s:") {
		return key[2:]
	}
	return ""
}

// pendingFor is a call in flight as the processor held it: what its headers say.
func pendingFor(c *canon.Request) canon.Pending {
	h := map[string]string{}
	for k, v := range map[string]string{
		"x-claude-code-session-id": c.SessionID, "x-claude-code-prompt-id": c.PromptID,
		"x-claude-code-request-class": c.RequestClass, "x-client-request-id": c.ClientRequestID,
		"x-claude-code-agent-type": c.AgentType,
	} {
		if v != "" {
			h[k] = v
		}
	}
	cj, _ := json.Marshal(h)
	return canon.Pending{CorrelationID: c.CallID, SeenNS: c.StartNS, StartNS: c.StartNS, Host: c.Host,
		Method: c.Method, Path: c.Path, Synthetic: c.Synthetic, KeyHash: c.KeyHash, RequestID: c.RequestID,
		ClientJSON: string(cj), ResponseStartNS: c.ResponseStartNS}
}
