package process

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Tables is every shaped table as committed, each sorted by its primary key.
type Tables struct {
	Requests []Request
	Events   []Event
	Sessions []Session
	Prompts  []Prompt
	Devices  []PersonDevice
	People   []Person
	Ledger   []Ledger
	Accounts []Account
	// RequestRows is derived (requestrows.go): equal when the tables it's built from are.
	RequestRows  []RequestRow
	PersonLast   []PersonLast
	PromptRows   []PromptRow
	PromptSteps  []PromptStep
	SessionRows  []SessionRow
	SessionMarks []SessionMark
	Presence     []PresenceSegment
	EventRows    []EventRow
	SessionTools []SessionTool
	SessionReqs  []SessionRequestRow
	PersonMap    []PersonMapRow
	PersonSess   []PersonSession
	AcctFacts    []AccountFact
	AcctDevices  []AccountDevice
	CallTools    []CallToolUse
}

// ReadTables reads the tables the manifest under prefix names.
func ReadTables(ctx context.Context, s Store, prefix string) (Tables, error) {
	var out Tables
	b, err := s.Get(ctx, strings.Trim(prefix, "/")+"/manifest.json")
	if err != nil {
		return out, fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return out, err
	}
	if err := readInto(ctx, s, m.Facts["requests"], &out.Requests); err != nil {
		return out, err
	}
	if err := readInto(ctx, s, m.Facts["events"], &out.Events); err != nil {
		return out, err
	}
	if err := readInto(ctx, s, m.Facts["ledger"], &out.Ledger); err != nil {
		return out, err
	}
	for table, into := range map[string]func([]string) error{
		"sessions":          func(k []string) error { return readInto(ctx, s, k, &out.Sessions) },
		"prompts":           func(k []string) error { return readInto(ctx, s, k, &out.Prompts) },
		"person_devices":    func(k []string) error { return readInto(ctx, s, k, &out.Devices) },
		"people":            func(k []string) error { return readInto(ctx, s, k, &out.People) },
		"accounts":          func(k []string) error { return readInto(ctx, s, k, &out.Accounts) },
		"request_rows":      func(k []string) error { return readInto(ctx, s, k, &out.RequestRows) },
		"person_last":       func(k []string) error { return readInto(ctx, s, k, &out.PersonLast) },
		"prompt_rows":       func(k []string) error { return readInto(ctx, s, k, &out.PromptRows) },
		"prompt_steps":      func(k []string) error { return readInto(ctx, s, k, &out.PromptSteps) },
		"session_rows":      func(k []string) error { return readInto(ctx, s, k, &out.SessionRows) },
		"session_marks":     func(k []string) error { return readInto(ctx, s, k, &out.SessionMarks) },
		"presence_segments": func(k []string) error { return readInto(ctx, s, k, &out.Presence) },
		"event_rows":        func(k []string) error { return readInto(ctx, s, k, &out.EventRows) },
		"session_tools":     func(k []string) error { return readInto(ctx, s, k, &out.SessionTools) },
		"session_request_rows": func(k []string) error {
			return readInto(ctx, s, k, &out.SessionReqs)
		},
		"person_map":      func(k []string) error { return readInto(ctx, s, k, &out.PersonMap) },
		"person_sessions": func(k []string) error { return readInto(ctx, s, k, &out.PersonSess) },
		"account_facts":   func(k []string) error { return readInto(ctx, s, k, &out.AcctFacts) },
		"account_devices": func(k []string) error { return readInto(ctx, s, k, &out.AcctDevices) },
		"call_tool_uses":  func(k []string) error { return readInto(ctx, s, k, &out.CallTools) },
	} {
		if k := m.Snapshots[table]; k != "" {
			if err := into([]string{k}); err != nil {
				return out, err
			}
		}
	}
	sort.Slice(out.Requests, func(i, j int) bool { return out.Requests[i].CallID < out.Requests[j].CallID })
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].EventKey < out.Events[j].EventKey })
	sort.Slice(out.Ledger, func(i, j int) bool { return out.Ledger[i].RawKey < out.Ledger[j].RawKey })
	return out, nil
}

func readInto[T any](ctx context.Context, s Store, keys []string, into *[]T) error {
	for _, k := range keys {
		rows, err := readParquet[T](ctx, s, k)
		if err != nil {
			return fmt.Errorf("read %s: %w", k, err)
		}
		*into = append(*into, rows...)
	}
	return nil
}

// WithoutLineage clears what depends on how the raw rows were split into files
// (source files, the ledger), to compare tables built from the same rows
// uploaded differently.
func (t Tables) WithoutLineage() Tables {
	t.Requests = append([]Request(nil), t.Requests...)
	for i := range t.Requests {
		t.Requests[i].SourceFile, t.Requests[i].FirstFile = "", ""
	}
	t.Events = append([]Event(nil), t.Events...)
	for i := range t.Events {
		t.Events[i].SourceFile = ""
	}
	t.Ledger = nil
	return t
}

// Diff lists how two sets of tables differ, by table and primary key (at most
// `limit` lines per table). Empty when they're equal.
func Diff(got, want Tables, limit int) []string {
	var out []string
	add := func(table string, lines []string) {
		if len(lines) > limit {
			lines = append(lines[:limit], fmt.Sprintf("… and %d more", len(lines)-limit))
		}
		for _, l := range lines {
			out = append(out, table+": "+l)
		}
	}
	add("requests", diffRows(got.Requests, want.Requests, func(r Request) string { return r.CallID }))
	add("events", diffRows(got.Events, want.Events, func(e Event) string { return e.EventKey }))
	add("sessions", diffRows(got.Sessions, want.Sessions, func(s Session) string { return s.SessionID }))
	add("prompts", diffRows(got.Prompts, want.Prompts, func(p Prompt) string { return p.PromptID }))
	add("person_devices", diffRows(got.Devices, want.Devices, func(d PersonDevice) string { return d.PersonID + "|" + d.DeviceID }))
	add("people", diffRows(got.People, want.People, func(p Person) string { return p.PersonID }))
	add("ledger", diffRows(got.Ledger, want.Ledger, func(l Ledger) string { return l.RawKey }))
	add("accounts", diffRows(got.Accounts, want.Accounts, func(a Account) string { return a.AccountID }))
	add("request_rows", diffRows(got.RequestRows, want.RequestRows, func(r RequestRow) string { return r.PersonID + "|" + r.RequestID }))
	add("person_last", diffRows(got.PersonLast, want.PersonLast, func(r PersonLast) string { return r.PersonID }))
	add("prompt_rows", diffRows(got.PromptRows, want.PromptRows, func(r PromptRow) string {
		return r.SessionID + "|" + strconv.FormatInt(r.Seq, 10)
	}))
	add("prompt_steps", diffRows(got.PromptSteps, want.PromptSteps, func(r PromptStep) string {
		return r.SessionID + "|" + strconv.FormatInt(r.Seq, 10) + "|" + strconv.FormatInt(r.Step, 10)
	}))
	add("session_rows", diffRows(got.SessionRows, want.SessionRows, func(r SessionRow) string { return r.SessionID }))
	add("session_marks", diffRows(got.SessionMarks, want.SessionMarks, func(r SessionMark) string {
		return r.SessionID + "|" + strconv.FormatInt(r.Seq, 10)
	}))
	add("presence_segments", diffRows(got.Presence, want.Presence, func(r PresenceSegment) string {
		return r.SessionID + "|" + strconv.FormatInt(r.Seq, 10)
	}))
	add("event_rows", diffRows(got.EventRows, want.EventRows, func(r EventRow) string {
		return r.SessionID + "|" + strconv.FormatInt(r.Seq, 10)
	}))
	add("session_tools", diffRows(got.SessionTools, want.SessionTools, func(r SessionTool) string {
		return r.SessionID + "|" + r.Kind + "|" + r.Server + "|" + r.Tool
	}))
	add("session_request_rows", diffRows(got.SessionReqs, want.SessionReqs, func(r SessionRequestRow) string {
		return r.Scope + "|" + strconv.FormatInt(r.Seq, 10)
	}))
	add("person_map", diffRows(got.PersonMap, want.PersonMap, func(r PersonMapRow) string { return r.PersonID + "|" + r.AccountID }))
	add("person_sessions", diffRows(got.PersonSess, want.PersonSess, func(r PersonSession) string { return r.PersonID + "|" + r.SessionID }))
	add("account_facts", diffRows(got.AcctFacts, want.AcctFacts, func(r AccountFact) string { return r.AccountID }))
	add("account_devices", diffRows(got.AcctDevices, want.AcctDevices, func(r AccountDevice) string { return r.AccountID + "|" + r.DeviceID }))
	add("call_tool_uses", diffRows(got.CallTools, want.CallTools, func(r CallToolUse) string {
		return r.SessionID + "|" + strconv.FormatInt(r.TsNS, 10) + "|" + r.Kind + "|" + r.Server + "|" + r.Tool
	}))
	return out
}

func diffRows[T any](got, want []T, key func(T) string) []string {
	g, w := map[string]T{}, map[string]T{}
	var keys []string
	for _, r := range got {
		k := key(r)
		if _, dup := g[k]; dup {
			keys = append(keys, k+"\x00dup")
		}
		g[k] = r
	}
	for _, r := range want {
		w[key(r)] = r
	}
	for k := range g {
		keys = append(keys, k)
	}
	for k := range w {
		if _, ok := g[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if strings.HasSuffix(k, "\x00dup") {
			out = append(out, fmt.Sprintf("%s: written more than once", strings.TrimSuffix(k, "\x00dup")))
			continue
		}
		gr, gok := g[k]
		wr, wok := w[k]
		switch {
		case !wok:
			out = append(out, fmt.Sprintf("%s: extra", k))
		case !gok:
			out = append(out, fmt.Sprintf("%s: missing", k))
		case !reflect.DeepEqual(gr, wr):
			out = append(out, fmt.Sprintf("%s: %s", k, fieldDiff(gr, wr)))
		}
	}
	return out
}

func fieldDiff(got, want any) string {
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	var parts []string
	for i := 0; i < gv.NumField(); i++ {
		if g, w := gv.Field(i).Interface(), wv.Field(i).Interface(); !reflect.DeepEqual(g, w) {
			parts = append(parts, fmt.Sprintf("%s %v, want %v", gv.Type().Field(i).Name, g, w))
		}
	}
	return strings.Join(parts, "; ")
}

// KeyReport is what CheckKeys found.
type KeyReport struct {
	Problems []string // duplicate primary keys, references that resolve to nothing
	// Analytics events whose request_id matches no captured call: calls the
	// client reported that the proxy never saw (coverage gaps), not errors.
	UncapturedCalls int
	// Analytics events whose carrying request has no row (its headers and
	// ending were never captured).
	UncarriedEvents int
}

// CheckKeys checks every primary key is unique and every non-null reference
// resolves (docs/ui/BUILD_PLAN.md §7.5).
func CheckKeys(t Tables) KeyReport {
	var r KeyReport
	unique := func(table string, keys []string) map[string]bool {
		set := map[string]bool{}
		for _, k := range keys {
			if set[k] {
				r.Problems = append(r.Problems, fmt.Sprintf("%s: duplicate key %q", table, k))
			}
			set[k] = true
		}
		return set
	}
	calls := unique("requests", keysOf(t.Requests, func(x Request) string { return x.CallID }))
	sessions := unique("sessions", keysOf(t.Sessions, func(x Session) string { return x.SessionID }))
	prompts := unique("prompts", keysOf(t.Prompts, func(x Prompt) string { return x.PromptID }))
	people := unique("people", keysOf(t.People, func(x Person) string { return x.PersonID }))
	unique("events", keysOf(t.Events, func(x Event) string { return x.EventKey }))
	unique("person_devices", keysOf(t.Devices, func(x PersonDevice) string { return x.PersonID + "|" + x.DeviceID }))
	unique("ledger", keysOf(t.Ledger, func(x Ledger) string { return x.RawKey }))
	requestIDs := map[string]bool{}
	for _, x := range t.Requests {
		if x.RequestID != "" && !x.Synthetic {
			requestIDs[x.RequestID] = true
		}
	}
	ref := func(what, key string, set map[string]bool) {
		if key != "" && !set[key] {
			r.Problems = append(r.Problems, fmt.Sprintf("%s %q resolves to nothing", what, key))
		}
	}
	for _, x := range t.Requests {
		if isModelCall(&x) {
			ref("requests.session_id", x.SessionID, sessions)
			ref("requests.prompt_id", x.PromptID, prompts)
			ref("requests.person_id", x.PersonID, people)
		}
	}
	for _, e := range t.Events {
		if (e.Source == "claude_code" || e.Source == "codex") && e.SessionID != "" {
			ref("events.session_id", e.SessionID, sessions)
			ref("events.prompt_id", e.PromptID, prompts)
			ref("events.person_id", e.PersonID, people)
		}
		if !calls[e.CorrelationID] {
			r.UncarriedEvents++
		}
		if e.RequestID != "" && !requestIDs[e.RequestID] {
			r.UncapturedCalls++
		}
	}
	for _, s := range t.Sessions {
		ref("sessions.person_id", s.PersonID, people)
	}
	for _, p := range t.Prompts {
		ref("prompts.session_id", p.SessionID, sessions)
	}
	for _, d := range t.Devices {
		ref("person_devices.person_id", d.PersonID, people)
	}
	return r
}

func keysOf[T any](xs []T, f func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}
