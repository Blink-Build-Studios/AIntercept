// Package flush writes records to the v9 fact tables (docs/PIPELINE.md §4):
// a slice of records, collapsed by key in Go, then one transaction with one
// unnest upsert per table, rows sorted by key, and the rollup rows they touch
// marked dirty. Every upsert is keyed and order-free, so re-running a flush,
// or running flushes in any order, gives the same tables.
package flush

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

const minuteNS = int64(60e9)

// Rows are a flush's records collapsed by key, each table's sorted by key.
type Rows struct {
	Calls              []Call
	Events             []Event
	Accounts           []Account
	SessionAccounts    []Span // (session, account)
	SessionCredentials []Span // (session, key hash)
	Devices            []Device
	Parents            []Parent
	Dirty              []Dirty
	// ReportIDs are the model calls' request and client request ids: the
	// minutes of their reports are marked dirty too (§4.3).
	ReportIDs []string
	// ReportedIDs are the reports' (Claude Code's tengu_api_success) request
	// and client request ids: the minutes of their calls are marked dirty too,
	// so a call whose report came later stops counting as unreported.
	ReportedIDs []string
	// StepIDs are the request ids of Claude Code's tool events, and ToolUseIDs
	// the tool use ids of its Bash events: a step reads them by those ids, not
	// by its prompt, so the prompts of their calls are marked dirty too (with
	// the reports' ReportedIDs).
	StepIDs, ToolUseIDs []string
	// ResultIDs are the tool call ids whose results the calls carry back
	// (detail's results): a step's exit codes are its session's first result
	// for each of its tool calls, which a later prompt's call can carry, so
	// the prompts of the calls that made those tool calls are marked dirty too.
	ResultIDs []string
	LastNS    int64 // the newest time in the records
}

// Call is a calls row: the record's request, and what's derived at insert.
type Call struct {
	R           canon.Request
	Scope, Kind string
	BatchEvents int64
	BatchName   string
	BatchOther  []string
	// Raw is the call's member in the raw file being loaded (WriteTx; RawKey
	// "": none).
	RawKey            string
	RawOffset, RawLen int64
}

// Event is an events row.
type Event struct {
	Key, Batch      string
	N, CCN          int64
	KeyHash         string
	E               canon.Event
	Summary, Marker string
	Internal        bool
}

// Newest is a value and when it was seen: the newest sighting wins, the
// greater value at the same time.
type Newest struct {
	V  string
	NS int64
}

func (n *Newest) offer(v string, ns int64) {
	if v != "" && (n.V == "" || ns > n.NS || ns == n.NS && v > n.V) {
		*n = Newest{v, ns}
	}
}

// Account is an accounts row.
type Account struct {
	ID                             string
	Email, Name, Org, Plan, CodexP Newest
	Seen                           int64
}

// Span is a (session, account) or (session, key hash) row: first and last seen.
type Span struct {
	A, B        string
	First, Last int64
}

// Device is an account_devices row.
type Device struct {
	Account, Device                           string
	OnEvents                                  bool
	Platform, Terminal, Shell, Agent, Version Newest
	First, Last                               int64
}

// Parent is a session_edges row: a session and the one it came from.
type Parent struct {
	Child, Parent, Kind string
	First               int64
}

// Dirty is a rollup row to recompute (§5.3).
type Dirty struct{ Kind, Key string }

// Collapse merges records by key: an end wins over a start, the earliest copy
// of an event wins, sightings keep the newest value, spans the earliest and
// latest times.
func Collapse(recs []records.Record) Rows {
	calls := map[string]*Call{}
	evs := map[string]*Event{}
	accts := map[string]*Account{}
	sessAccts, sessCreds := map[[2]string]*Span{}, map[[2]string]*Span{}
	devs := map[[2]string]*Device{}
	parents := map[[2]string]*Parent{}
	dirty := map[Dirty]bool{}
	reports, reported := map[string]bool{}, map[string]bool{}
	stepIDs, toolUseIDs, resultIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var last int64

	account := func(id string, ns int64) *Account {
		a := accts[id]
		if a == nil {
			a = &Account{ID: id}
			accts[id] = a
		}
		a.Seen = max(a.Seen, ns)
		return a
	}
	span := func(m map[[2]string]*Span, a, b string, ns int64) {
		k := [2]string{a, b}
		if s := m[k]; s != nil {
			s.First, s.Last = min(s.First, ns), max(s.Last, ns)
		} else {
			m[k] = &Span{A: a, B: b, First: ns, Last: ns}
		}
	}
	device := func(acct, dev string, ns int64) *Device {
		k := [2]string{acct, dev}
		d := devs[k]
		if d == nil {
			d = &Device{Account: acct, Device: dev, First: ns, Last: ns}
			devs[k] = d
		}
		d.First, d.Last = min(d.First, ns), max(d.Last, ns)
		return d
	}
	minute := func(kind, id string, ns int64) {
		if id != "" {
			dirty[Dirty{kind, fmt.Sprintf("%s/%d", id, ns-ns%minuteNS)}] = true
		}
	}

	for i := range recs {
		r := &recs[i]
		switch {
		case r.Kind == records.KindAccount && r.Account != nil:
			s := r.Account
			a := account(s.AccountID, s.NS)
			a.Email.offer(s.Email, s.NS)
			a.Name.offer(s.Name, s.NS)
			a.Org.offer(s.Org, s.NS)
			last = max(last, s.NS)

		case r.Kind == records.KindClientEvent && r.Event != nil:
			e := r.Event
			key := e.EventKey
			if e.ClientEventID != "" {
				key = e.Source + "/" + e.ClientEventID
			}
			row := &Event{Key: key, Batch: r.BatchID, N: r.BatchN, CCN: r.BatchCCN, E: *e}
			if r.Call != nil {
				row.KeyHash = r.Call.KeyHash
			}
			if cur := evs[key]; cur == nil || eventBefore(row, cur) {
				// Summary, internal and marker as the Events tab's rows derive them
				// (process/eventrows.go EventFactsOf: summaryOf, isInternal, the marker).
				f := process.EventFactsOf(*e).Row
				row.Summary, row.Internal, row.Marker = f.Summary, f.Internal, f.Marker
				evs[key] = row
			}
			ns := e.NS
			last = max(last, e.RowNS, ns)
			minute("minute", e.SessionID, ns)
			if e.PromptID != "" {
				dirty[Dirty{"prompt", e.PromptID}] = true
			} else if e.Source == canon.ProductClaudeCode && e.Name == "tengu_input_prompt" && e.SessionID != "" {
				// A typed prompt with no prompt id is its own prompt, keyed by its
				// place in its batch (process/promptrows.go rows: "input:" + its id).
				dirty[Dirty{"prompt", fmt.Sprintf("input:%s:%d", r.BatchID, r.BatchCCN)}] = true
			}
			if e.Source == canon.ProductClaudeCode {
				switch e.Name {
				case "tengu_tool_use_success", "tengu_tool_use_error", "tengu_tool_use_progress":
					if e.RequestID != "" {
						stepIDs[e.RequestID] = true
					}
				case "tengu_bash_tool_command_executed":
					if e.ToolUseID != "" {
						toolUseIDs[e.ToolUseID] = true
					}
				}
			}
			if e.Source == canon.ProductClaudeCode && e.Name == "tengu_api_success" {
				for _, id := range []string{e.RequestID, e.ClientRequestID} {
					if id != "" {
						reported[id] = true
					}
				}
			}
			// A session's credentials are its visible calls' and those of the analytics posts that
			// carried its Claude Code events (process/requestrows.go: callHashes, corrHashes).
			if e.Source == canon.ProductClaudeCode && e.SessionID != "" && row.KeyHash != "" {
				span(sessCreds, e.SessionID, row.KeyHash, ns)
			}
			if e.Source != canon.ProductClaudeCode || e.PersonID == "" {
				continue
			}
			// What an account's Claude Code events say about it (process/personmap.go acctEvents).
			if e.SessionID != "" {
				span(sessAccts, e.SessionID, e.PersonID, ns)
			}
			if plan, ok := process.SubscriptionType(e.MetadataJSON); ok {
				account(e.PersonID, ns).Plan.offer(plan, ns)
			}
			if e.OrgID != "" { // the org the person pages show (process/personmap.go acctEvents)
				account(e.PersonID, ns).Org.offer(e.OrgID, ns)
			}
			if e.DeviceID != "" {
				d := device(e.PersonID, e.DeviceID, ns)
				d.OnEvents = true
				if v, ok := process.EnvFields(e.EnvJSON); ok[0] || ok[1] || ok[2] {
					d.Platform.offer(v[0], ns)
					d.Terminal.offer(v[1], ns)
					d.Shell.offer(v[2], ns)
				}
			}

		case r.Call != nil:
			c := r.Call
			// kind: the Requests tab's channel (process/requestrows.go kindOf).
			row := &Call{R: *c, Scope: c.SessionID, Kind: process.RequestKind(c.Path),
				BatchEvents: r.BatchEvents, BatchName: r.BatchName, BatchOther: r.BatchOtherSessions}
			if row.Scope == "" { // an analytics post: its batch's earliest session (records/assemble.go event, txSession)
				row.Scope = r.BatchSession
			}
			if cur := calls[c.CallID]; cur == nil {
				calls[c.CallID] = row
			} else if c.EndNS != 0 && (cur.R.EndNS == 0 || c.EndNS > cur.R.EndNS) {
				if row.R.StartNS == 0 {
					row.R.StartNS = cur.R.StartNS
				}
				calls[c.CallID] = row
			}
			at := c.StartNS
			if at == 0 {
				at = c.EndNS
			}
			last = max(last, at, c.EndNS)
			minute("minute", c.SessionID, at)
			if row.Scope != c.SessionID {
				minute("minute", row.Scope, at)
			}
			if agentCall(c) { // the credential rollups count coding agents' calls (refresh: agentCalls)
				dirty[Dirty{"cred_minute", fmt.Sprintf("%s/%d", c.KeyHash, at-at%minuteNS)}] = true
			}
			if c.PromptID != "" {
				dirty[Dirty{"prompt", c.PromptID}] = true
			}
			if c.SessionID != "" {
				for _, id := range resultKeys(c.DetailJSON) {
					resultIDs[id] = true
				}
			}
			if row.Kind == "model" {
				for _, id := range []string{c.RequestID, c.ClientRequestID} {
					if id != "" {
						reports[id] = true
					}
				}
			}
			if !canon.IsModelCall(c) {
				continue
			}
			// A coding agent's model call (the API's agent_calls; process/personmap.go).
			if c.Product == canon.ProductCodex && c.PersonID != "" {
				if plan, ok := process.CodexPlanOf(c.ProviderJSON); ok {
					account(c.PersonID, at).CodexP.offer(plan, at)
				}
			}
			if c.Product == canon.ProductCodex {
				if kind, parent := process.CodexLineage(c.ExtJSON); parent != "" {
					k := [2]string{c.SessionID, parent}
					if p := parents[k]; p == nil {
						parents[k] = &Parent{Child: c.SessionID, Parent: parent, Kind: kind, First: at}
					} else {
						p.Kind, p.First = min(p.Kind, kind), min(p.First, at)
					}
				}
			}
			if c.HiddenKind != "" { // the cc_calls view: the calls the user sees
				continue
			}
			if c.PersonID != "" {
				span(sessAccts, c.SessionID, c.PersonID, at)
				if c.DeviceID != "" {
					d := device(c.PersonID, c.DeviceID, at)
					d.Agent.offer(c.Product, at)
					d.Version.offer(c.ClientVersion, at)
				}
			}
			if c.KeyHash != "" {
				span(sessCreds, c.SessionID, c.KeyHash, at)
			}
		}
	}

	out := Rows{LastNS: last}
	out.Calls = sortedBy(calls, func(c *Call) string { return c.R.CallID })
	out.Events = sortedBy(evs, func(e *Event) string { return e.Key })
	out.Accounts = sortedBy(accts, func(a *Account) string { return a.ID })
	out.SessionAccounts = sortedBy(sessAccts, func(s *Span) string { return s.A + "\x00" + s.B })
	out.SessionCredentials = sortedBy(sessCreds, func(s *Span) string { return s.A + "\x00" + s.B })
	out.Devices = sortedBy(devs, func(d *Device) string { return d.Account + "\x00" + d.Device })
	out.Parents = sortedBy(parents, func(p *Parent) string { return p.Child + "\x00" + p.Parent })
	for d := range dirty {
		out.Dirty = append(out.Dirty, d)
	}
	slices.SortFunc(out.Dirty, func(a, b Dirty) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Key, b.Key)) })
	for id := range reports {
		out.ReportIDs = append(out.ReportIDs, id)
	}
	slices.Sort(out.ReportIDs)
	for id := range reported {
		out.ReportedIDs = append(out.ReportedIDs, id)
	}
	slices.Sort(out.ReportedIDs)
	for id := range stepIDs {
		out.StepIDs = append(out.StepIDs, id)
	}
	slices.Sort(out.StepIDs)
	for id := range toolUseIDs {
		out.ToolUseIDs = append(out.ToolUseIDs, id)
	}
	slices.Sort(out.ToolUseIDs)
	for id := range resultIDs {
		out.ResultIDs = append(out.ResultIDs, id)
	}
	slices.Sort(out.ResultIDs)
	return out
}

// agentCall is whether a call is Claude Code's or Codex's and not a probe (the
// API's agent_calls: render/rules.go agentCall), a call to Anthropic's API with
// no product being Claude Code's.
func agentCall(c *canon.Request) bool {
	p := c.Product
	if p == "" && c.Host == "api.anthropic.com" {
		p = canon.ProductClaudeCode
	}
	return !c.Synthetic && (p == canon.ProductClaudeCode || p == canon.ProductCodex)
}

// eventBefore is whether a is an earlier copy of an event than b: a resent
// batch's copy loses to the first (its row time, then its batch and place).
func eventBefore(a, b *Event) bool {
	return cmp.Or(cmp.Compare(a.E.RowNS, b.E.RowNS), cmp.Compare(a.Batch, b.Batch), cmp.Compare(a.N, b.N)) < 0
}

func sortedBy[K comparable, T any](m map[K]*T, key func(*T) string) []T {
	out := make([]T, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	slices.SortFunc(out, func(a, b T) int { return strings.Compare(key(&a), key(&b)) })
	return out
}

// resultKeys is the tool call ids a call's recorded detail carries results
// for (events.CallDetail's results), in no order; none for a detail without
// them.
func resultKeys(detail string) []string {
	if !strings.Contains(detail, `"results"`) {
		return nil
	}
	var d struct {
		Results map[string]json.RawMessage `json:"results"`
	}
	if json.Unmarshal([]byte(detail), &d) != nil {
		return nil
	}
	out := make([]string, 0, len(d.Results))
	for id := range d.Results {
		out = append(out, id)
	}
	return out
}
