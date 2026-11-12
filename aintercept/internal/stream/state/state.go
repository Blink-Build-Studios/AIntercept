package state

import (
	"bytes"
	"encoding/gob"
	"sort"

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
	rows rowSet
}

// New is an empty state for key.
func New(key string) *KeyState {
	return &KeyState{Key: key, seen: map[string]bool{}, ses: newSession(), rows: rowSet{},
		segs: newOmap[int64, Presence](func(a, b int64) bool { return a < b })}
}

// Apply folds one record in and says which rows changed.
func (s *KeyState) Apply(r records.Record) Delta {
	if s.seen[r.RecordID] {
		return Delta{Cases: []string{CaseDuplicate}}
	}
	s.seen[r.RecordID] = true
	var d Delta
	_ = r // not built yet: the tests below define it
	return d
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
}

// Encode is the state as bytes, for a checkpoint.
func Encode(s *KeyState) ([]byte, error) {
	snap := snapshot{Key: s.Key, Session: s.ses.snap()}
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
	s.rebuild()
	return s, nil
}

// rebuild recomputes every row from the indexes, after Decode: the rows were
// written before, so the changes are dropped.
func (s *KeyState) rebuild() {
	var d Delta
	sid := sessionOf(s.Key)
	for _, w := range s.ses.sleep.list() {
		s.rows.put(&d, TablePresence, idOf("z:", w.A), Presence{sid, 1, w.A, w.B, "asleep"})
	}
	now := s.ses.spanNow()
	s.ses.before = now
	if now.ok {
		s.recut(&d, sid, now.first, now.last, now)
	}
}
