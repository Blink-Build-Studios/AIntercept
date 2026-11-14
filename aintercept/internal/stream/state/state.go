package state

import (
	"bytes"
	"encoding/gob"
	"errors"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"reflect"
	"sort"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/zcodec"
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
	sr   *sesRows
	rows rowSet
	open *openCalls // calls waiting for their ending, by when they expire (expire.go)
	// journal is the records applied since the last Encode: a checkpoint is
	// them, so it holds what changed, never the key's history.
	journal []records.Record
	// ext is the render's external inputs (SetExternal), extJournal their
	// changes since the last Encode.
	ext        map[string]any
	extJournal []External
}

// External is one of a key's external inputs: what the render reads from
// outside the key's records (a call's detail object, other keys' rows of its
// batches), kept with the state so the render stays a function of it. A cold
// load gets them back from the checkpoint, so its baseline is the render that
// was committed. Value nil removes it.
type External struct {
	ID    string
	Value any
}

// External is the input stored under id.
func (s *KeyState) External(id string) (any, bool) {
	v, ok := s.ext[id]
	return v, ok
}

// Externals is every external input, by id (read only).
func (s *KeyState) Externals() map[string]any { return s.ext }

// ExternalsChanged is the ids of the external inputs set since the last
// Encode (a run's: the worker encodes after it renders).
func (s *KeyState) ExternalsChanged() []string {
	out := make([]string, 0, len(s.extJournal))
	for _, x := range s.extJournal {
		out = append(out, x.ID)
	}
	return out
}

// SetExternal stores an external input (nil removes it) and journals it,
// unless it's unchanged; it reports whether it changed. Its value's type must
// be gob-registered.
func (s *KeyState) SetExternal(id string, v any) bool {
	cur, ok := s.ext[id]
	if v == nil && !ok || v != nil && ok && reflect.DeepEqual(cur, v) {
		return false
	}
	s.setExt(id, v)
	s.extJournal = append(s.extJournal, External{ID: id, Value: v})
	return true
}

func (s *KeyState) setExt(id string, v any) {
	if v == nil {
		delete(s.ext, id)
		return
	}
	s.ext[id] = v
}

// New is an empty state for key.
func New(key string) *KeyState {
	return &KeyState{Key: key, seen: map[string]bool{}, ses: newSession(), ev: newEventsState(), dm: newDimsState(), pr: newPromptsState(), rq: newReqState(), sr: newSesRows(), rows: rowSet{}, open: newOpenCalls(), ext: map[string]any{},
		segs: newOmap[int64, Presence](func(a, b int64) bool { return a < b })}
}

// Apply folds one record in and says which rows changed.
func (s *KeyState) Apply(r records.Record) Delta {
	if s.seen[r.RecordID] {
		return Delta{Cases: []string{CaseDuplicate}}
	}
	s.seen[r.RecordID] = true
	s.journal = append(s.journal, r)
	defer process.HoldFields(jsonOf(&r)...)()
	var d Delta
	ch := s.ses.apply(r, &d)
	s.presence(&d, ch)
	s.events(r, &d)
	s.dims(r, &d)
	s.prompts(r, &d)
	s.requests(r, &d)
	s.sessionRows(r, &d)
	s.callTools(r, &d)
	s.facts(r, &d)
	d.Changes = lastChanges(d.Changes)
	sort.Slice(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		return a.Table < b.Table || a.Table == b.Table && a.ID < b.ID
	})
	return d
}

// jsonOf is the record's JSON objects the tables read fields of.
func jsonOf(r *records.Record) []string {
	switch {
	case r.Event != nil:
		return []string{r.Event.MetadataJSON, r.Event.EnvJSON}
	case r.Call != nil:
		return []string{r.Call.ProviderJSON, r.Call.ExtJSON}
	}
	return nil
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

// RowTypes is every stored row type: each field has a snake_case parquet tag
// (rowtypes_test.go), so table files name their columns.
var RowTypes = []any{Presence{}, Event{}, RequestLink{}, PromptIndex{}, CodexTurn{}, process.SessionTool{},
	process.Account{}, SessionAccount{}, SessionVisible{}, SessionPerson{}, PersonDevice{}, SessionCredential{},
	AccountEvents{}, AccountDevice{}, CodexPlan{},
	PromptItem{}, StepRow{}, StepToolsRow{}, PromptSession{}, MarkRow{}, MarkPromptIndex{},
	ReqTx{}, ReportRow{}, MissedRow{}, BatchMember{}, SessionLast{}, CallToolUse{},
	canon.Request{}, canon.Pending{}, canon.Event{}, Probes{}}

func init() {
	for _, v := range RowTypes {
		gob.Register(v)
	}
}

// A checkpoint is a chunk of the key's journal: the records it applied since
// the last one, in the order it applied them. A key is loaded back by applying
// its chunks' records again, oldest chunk first: Apply is deterministic, so the
// state, its indexes and its rows come back exactly. A checkpoint's size is
// what changed, however long the key's history; loading costs the same per
// record held. The chunk is the journal's gob, compressed (zstd): records
// repeat their event metadata and call headers, so it's a small fraction of
// the gob. A chunk written before compression (plain gob) still loads.
type journalChunk struct {
	Key     string
	Records []records.Record
	// Externals are the external inputs set since the last checkpoint, in
	// order (a chunk from before them has none).
	Externals []External
}

// zstdMagic starts every zstd frame; a gob stream never starts with it (its
// first message is journalChunk's type, 0x3d 0x7f …).
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

func pack(c journalChunk) ([]byte, error) {
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(c); err != nil {
		return nil, err
	}
	return zcodec.Shared.Encode(nil, b.Bytes())
}

// unpack is a chunk's gob: decompressed, or as it is for a plain-gob chunk.
func unpack(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, zstdMagic) {
		return b, nil
	}
	return zcodec.Shared.Decode(nil, b)
}

func decodeChunk(b []byte) (journalChunk, error) {
	var c journalChunk
	raw, err := unpack(b)
	if err == nil {
		err = gob.NewDecoder(bytes.NewReader(raw)).Decode(&c)
	}
	return c, err
}

// Encode is the key's checkpoint: the records applied since the last Encode.
func Encode(s *KeyState) ([]byte, error) {
	b, err := pack(journalChunk{Key: s.Key, Records: s.journal, Externals: s.extJournal})
	if err == nil {
		s.journal, s.extJournal = nil, nil
	}
	return b, err
}

// Decode is a key's state from its checkpoints, oldest first.
func Decode(chunks ...[]byte) (*KeyState, error) {
	if len(chunks) == 0 {
		return nil, errors.New("state: no checkpoint to load")
	}
	var s *KeyState
	for _, b := range chunks {
		c, err := decodeChunk(b)
		if err != nil {
			return nil, err
		}
		if s == nil {
			s = New(c.Key)
		}
		for _, r := range c.Records {
			s.Apply(r)
		}
		for _, x := range c.Externals {
			s.setExt(x.ID, x.Value)
		}
	}
	s.journal, s.extJournal = nil, nil
	return s, nil
}

// Merge is one checkpoint holding the given ones' records, in order: what
// compaction keeps for a key. Of the external inputs it keeps each one's last
// value (a removal too: chunks before it may set the value), which is what
// Decode ends with, so a value set on every run doesn't pile up.
func Merge(chunks ...[]byte) ([]byte, error) {
	var out journalChunk
	last := map[string]int{}
	for _, b := range chunks {
		c, err := decodeChunk(b)
		if err != nil {
			return nil, err
		}
		if out.Key == "" {
			out.Key = c.Key
		}
		out.Records = append(out.Records, c.Records...)
		for _, x := range c.Externals {
			if i, ok := last[x.ID]; ok {
				out.Externals[i].Value = x.Value
				continue
			}
			last[x.ID] = len(out.Externals)
			out.Externals = append(out.Externals, x)
		}
	}
	return pack(out)
}

// Held is how many records the key holds in its journal (not yet checkpointed).
func (s *KeyState) Held() int { return len(s.journal) + len(s.extJournal) }
