package state

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The tables a key's rows go to.
const (
	TablePresence = "presence_segments"
)

// Change is one row of a Delta: Row is the row to upsert under ID, or nil to
// remove the row stored under ID.
type Change struct {
	Table string
	ID    string
	Row   any
}

// Delta is what one record changed: rows in (Table, ID) order, and the cases
// (branches) it took.
type Delta struct {
	Changes []Change
	Cases   []string
}

// KeyState is one key's state. Its fields are the key's indexes; callers treat
// it as opaque and persist it with Encode.
type KeyState struct {
	Key  string
	seen map[string]bool // record ids applied: a duplicate is a no-op
	ses  *session
	segs *omap[int64, Presence] // the span's focused and away segments, by start
	ev   *eventsState
	dm   *dimsState
	pr   *promptsState
	rq   *reqState
	rows rowSet
}

// New is an empty state for key.
func New(key string) *KeyState {
	return &KeyState{Key: key, seen: map[string]bool{}, ses: newSession(), ev: newEventsState(), dm: newDimsState(), pr: newPromptsState(), rq: newReqState(), rows: rowSet{},
		segs: newOmap[int64, Presence](func(a, b int64) bool { return a < b })}
}

// Apply folds one record in and says which rows changed.
func (s *KeyState) Apply(r records.Record) Delta {
	if s.seen[r.RecordID] {
		return Delta{Cases: []string{CaseDuplicate}}
	}
	s.seen[r.RecordID] = true
	var d Delta
	ch := s.ses.apply(r, &d)
	s.presence(&d, ch)
	s.events(r, &d)
	s.dims(r, &d)
	s.prompts(r, &d)
	s.requests(r, &d)
	d.Changes = lastChanges(d.Changes)
	sort.Slice(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		return a.Table < b.Table || a.Table == b.Table && a.ID < b.ID
	})
	return d
}

// lastChanges keeps each row's last change: one record can write a row more than
// once (a prompt its call and its hidden call both touch), and only the last is
// the row.
func lastChanges(cs []Change) []Change {
	if len(cs) < 2 {
		return cs
	}
	last := make(map[[2]string]int, len(cs))
	for i, c := range cs {
		last[[2]string{c.Table, c.ID}] = i
	}
	if len(last) == len(cs) {
		return cs
	}
	out := cs[:0:0]
	for i, c := range cs {
		if last[[2]string{c.Table, c.ID}] == i {
			out = append(out, c)
		}
	}
	return out
}

// rowSet is the rows a key has written, by table and id: what a change is
// compared with, so a Delta only names rows that really changed.
type rowSet map[string]map[string]any

func (rs rowSet) put(d *Delta, table, id string, row any) {
	t := rs[table]
	if t == nil {
		t = map[string]any{}
		rs[table] = t
	}
	if cur, ok := t[id]; ok && sameRow(cur, row) {
		return
	}
	t[id] = row
	d.Changes = append(d.Changes, Change{Table: table, ID: id, Row: row})
}

// sameRow is whether a row is unchanged: == for a comparable row (pointer fields
// compare as unchanged only when shared), else deep equality (a row with lists).
func sameRow(a, b any) bool {
	if ta := reflect.TypeOf(a); ta == reflect.TypeOf(b) && ta.Comparable() {
		return a == b
	}
	return reflect.DeepEqual(a, b)
}

func (rs rowSet) drop(d *Delta, table, id string) {
	if _, ok := rs[table][id]; !ok {
		return
	}
	delete(rs[table], id)
	d.Changes = append(d.Changes, Change{Table: table, ID: id})
}

// Rows is every row the key has written, by table and id.
func (s *KeyState) Rows() map[string]map[string]any { return s.rows }

// snapshot is what Encode writes: the records' projections. Decode rebuilds the
// indexes and rows from them.
type snapshot struct {
	Key     string
	Seen    []string
	Session sessionSnap
	Rows    map[string]map[string]any
	Links   map[string][]LinkEntry
	Tools   map[string]int64
	Turns   map[string]int64
	Dims    dimsSnap
	// Prompts is the records the prompt tables and marks hold: Decode applies
	// them again to rebuild their indexes.
	Prompts []records.Record
	Request reqSnap
}

// dimsSnap is the dimension indexes, for Encode.
type dimsSnap struct {
	Account *process.Account
	Visible bool
	Person  string
	Creds   map[string][]string
	CredOf  map[string]string
	AcctEv  []AccountEvents
	AcctDev []AccountDevice
	Codex   []CodexPlan
}

// LinkEntry is one visible call under a request id, for Encode.
type LinkEntry struct {
	TS     int64
	ID     string
	CallID string // the call it's from (its start's id, or its ending's)
	Link   RequestLink
}

// RowTypes is every stored row type: each field has a snake_case parquet tag
// (rowtypes_test.go), so table files name their columns.
var RowTypes = []any{Presence{}, Event{}, RequestLink{}, PromptIndex{}, CodexTurn{}, process.SessionTool{},
	process.Account{}, SessionAccount{}, SessionVisible{}, SessionPerson{}, PersonDevice{}, SessionCredential{},
	AccountEvents{}, AccountDevice{}, CodexPlan{},
	PromptItem{}, StepRow{}, PromptSession{}, MarkRow{}, MarkPromptIndex{},
	ReqTx{}, ReportRow{}, MissedRow{}, BatchMember{}, SessionLast{}}

func init() {
	for _, v := range RowTypes {
		gob.Register(v)
	}
}

// Encode is the state as bytes, for a checkpoint.
func Encode(s *KeyState) ([]byte, error) {
	snap := snapshot{Key: s.Key, Session: s.ses.snap(), Rows: s.rows, Links: map[string][]LinkEntry{},
		Tools: s.ev.tools, Turns: s.ev.turns, Dims: s.dm.snap(), Prompts: s.pr.snap(), Request: s.rq.snap()}
	byAt := map[string]string{}
	for call, la := range s.ev.callLink {
		byAt[la.rid+"\x00"+la.at.id] = call
	}
	for rid, m := range s.ev.links {
		m.All(func(k tsID, v RequestLink) bool {
			snap.Links[rid] = append(snap.Links[rid], LinkEntry{k.ts, k.id, byAt[rid+"\x00"+k.id], v})
			return true
		})
	}
	for id := range s.seen {
		snap.Seen = append(snap.Seen, id)
	}
	sort.Strings(snap.Seen)
	var b bytes.Buffer
	err := gob.NewEncoder(&b).Encode(snap)
	return b.Bytes(), err
}

// Decode is a state from Encode's bytes.
func Decode(b []byte) (*KeyState, error) {
	var snap snapshot
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&snap); err != nil {
		return nil, err
	}
	s := New(snap.Key)
	for _, id := range snap.Seen {
		s.seen[id] = true
	}
	s.ses = sessionFrom(snap.Session)
	s.ses.before = s.ses.spanNow()
	s.rows = snap.Rows
	if s.rows == nil {
		s.rows = rowSet{}
	}
	for id, row := range s.rows[TablePresence] {
		if strings.HasPrefix(id, "f:") {
			p := row.(Presence)
			s.segs.Set(p.FromNS, p)
		}
	}
	for rid, es := range snap.Links {
		m := newOmap[tsID, RequestLink](lessTsID)
		for _, e := range es {
			m.Set(tsID{e.TS, e.ID}, e.Link)
			if e.CallID != "" {
				s.ev.callLink[e.CallID] = linkAt{rid, tsID{e.TS, e.ID}}
			}
		}
		s.ev.links[rid] = m
	}
	if snap.Tools != nil {
		s.ev.tools = snap.Tools
	}
	if snap.Turns != nil {
		s.ev.turns = snap.Turns
	}
	s.dm = dimsFrom(snap.Dims)
	s.promptsFrom(snap.Prompts)
	s.reqFrom(snap.Request)
	return s, nil
}

func (x *dimsState) snap() dimsSnap {
	y := dimsSnap{Account: x.account, Visible: x.visible, Person: x.person, Creds: map[string][]string{}, CredOf: x.credOf}
	for h, set := range x.creds {
		for src := range set {
			y.Creds[h] = append(y.Creds[h], src)
		}
	}
	for _, v := range x.acctEv {
		y.AcctEv = append(y.AcctEv, *v)
	}
	for _, v := range x.acctDev {
		y.AcctDev = append(y.AcctDev, *v)
	}
	for _, v := range x.codex {
		y.Codex = append(y.Codex, *v)
	}
	return y
}

func dimsFrom(y dimsSnap) *dimsState {
	x := newDimsState()
	x.account, x.visible, x.person = y.Account, y.Visible, y.Person
	for h, srcs := range y.Creds {
		x.creds[h] = map[string]bool{}
		for _, src := range srcs {
			x.creds[h][src] = true
		}
	}
	for k, v := range y.CredOf {
		x.credOf[k] = v
	}
	for _, v := range y.AcctEv {
		v := v
		x.acctEv[v.AccountID] = &v
	}
	for _, v := range y.AcctDev {
		v := v
		x.acctDev[[2]string{v.AccountID, v.DeviceID}] = &v
	}
	for _, v := range y.Codex {
		v := v
		x.codex[v.AccountID] = &v
	}
	return x
}
