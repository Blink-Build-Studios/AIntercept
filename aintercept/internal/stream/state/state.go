package state

import (
	"bytes"
	"encoding/gob"
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
	rows rowSet
}

// New is an empty state for key.
func New(key string) *KeyState {
	return &KeyState{Key: key, seen: map[string]bool{}, ses: newSession(), ev: newEventsState(), rows: rowSet{},
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
	sort.Slice(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		return a.Table < b.Table || a.Table == b.Table && a.ID < b.ID
	})
	return d
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
	if cur, ok := t[id]; ok && cur == row {
		return
	}
	t[id] = row
	d.Changes = append(d.Changes, Change{Table: table, ID: id, Row: row})
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
}

// LinkEntry is one visible call under a request id, for Encode.
type LinkEntry struct {
	TS     int64
	ID     string
	CallID string // the call it's from (its start's id, or its ending's)
	Link   RequestLink
}

func init() {
	for _, v := range []any{Presence{}, Event{}, RequestLink{}, PromptIndex{}, CodexTurn{}, process.SessionTool{}} {
		gob.Register(v)
	}
}

// Encode is the state as bytes, for a checkpoint.
func Encode(s *KeyState) ([]byte, error) {
	snap := snapshot{Key: s.Key, Session: s.ses.snap(), Rows: s.rows, Links: map[string][]LinkEntry{},
		Tools: s.ev.tools, Turns: s.ev.turns}
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
	return s, nil
}
