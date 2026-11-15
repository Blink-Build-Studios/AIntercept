package state

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/snap"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/zcodec"
)

// A snapshot is a key's warm state (docs/PIPELINE.md §2, v8): the state with
// every index and running value, and what the render keeps between runs (its
// warm state, opaque here). A cold load decodes it, so it never replays the
// key's history or renders it.
//
// The key's journal (its chunks of records, Encode) stays its checkpoint: a
// snapshot holds the journal's first records (the state as of them), and a
// load decodes it and applies only the records after. So a snapshot is never
// the only copy of a key: one a later build can't read (its types changed,
// so its fingerprint did) is ignored, and the key loads from its journal.
//
// The bytes are snapMagic, the snapshot's type fingerprint (8 bytes), then
// the graph's encoding (internal/snap), compressed.

// snapMagic starts a snapshot: neither a zstd frame nor a gob stream starts
// with it.
var snapMagic = []byte("AISNAP1\n")

// ErrStaleSnapshot is a snapshot another build's types wrote.
var ErrStaleSnapshot = errors.New("state: a snapshot of other types")

// snapshot is what a snapshot holds.
type snapshot struct {
	State *KeyState
	// Warm is the render's warm state as of the same commit (nil: none).
	Warm any
}

var registerSnap = sync.OnceFunc(func() {
	snap.Register(RowTypes...)
	snap.Register(OtherParts{}, canon.Request{}, canon.Pending{}, canon.Event{})
	snap.RegisterFunc("state.lessInt64", lessInt64)
	snap.RegisterFunc("state.lessTsID", lessTsID)
	snap.RegisterFunc("state.lessEvOrd", lessEvOrd)
	snap.RegisterFunc("state.lessEv", lessEv)
	snap.RegisterFunc("state.lessPK", lessPK)
	snap.RegisterFunc("state.lessPKLastID", lessPKLastID)
})

// EncodeSnapshot is s's snapshot, with warm the render's warm state (its type
// registered with snap). It holds the records s has applied (Records), and
// leaves s's journal as it is: the journal chunk is Encode's.
func EncodeSnapshot(s *KeyState, warm any) ([]byte, error) {
	registerSnap()
	journal, extJournal := s.journal, s.extJournal
	s.journal, s.extJournal = nil, nil
	defer func() { s.journal, s.extJournal = journal, extJournal }()
	v := &snapshot{State: s, Warm: warm}
	var out bytes.Buffer
	out.Grow(512 * (s.Records() + 64)) // a snapshot is ~520 bytes a record held (the capture's heaviest)
	out.Write(snapMagic)
	out.Write(binary.LittleEndian.AppendUint64(nil, snap.Fingerprint(v)))
	// Compressed as it's encoded: the graph's bytes never sit in memory whole.
	z, err := zstd.NewWriter(&out, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	if err := snap.EncodeTo(z, v); err != nil {
		z.Close()
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// IsSnapshot is whether a chunk is a snapshot.
func IsSnapshot(b []byte) bool { return bytes.HasPrefix(b, snapMagic) }

func decodeSnapshot(b []byte) (*snapshot, error) {
	registerSnap()
	if !IsSnapshot(b) || len(b) < len(snapMagic)+8 {
		return nil, errors.New("state: not a snapshot")
	}
	var v snapshot
	if binary.LittleEndian.Uint64(b[len(snapMagic):]) != snap.Fingerprint(&v) {
		return nil, ErrStaleSnapshot
	}
	raw, err := zcodec.Shared.Decode(nil, b[len(snapMagic)+8:])
	if err != nil {
		return nil, fmt.Errorf("state: snapshot: %w", err)
	}
	if err := snap.Decode(raw, &v); err != nil {
		return nil, fmt.Errorf("state: snapshot: %w", err)
	}
	if v.State == nil {
		return nil, errors.New("state: a snapshot with no state")
	}
	return &v, nil
}

// Loaded is a key's state from its checkpoint.
type Loaded struct {
	State *KeyState
	// Warm is the render's warm state the snapshot held (nil: no snapshot,
	// or none in it).
	Warm any
	// Snapshot is whether a snapshot was decoded.
	Snapshot bool
	// Changes are what the records applied after the snapshot changed, each
	// (table, id)'s first Old and last Row: what Warm hasn't seen (none with
	// no snapshot).
	Changes []Change
	// Externals are the external inputs those chunks set.
	Externals []string
	// Replayed is how many records were applied (not decoded).
	Replayed int
	// ChunkRecords is each journal chunk's records (the ones skipped too).
	ChunkRecords []int64
}

// Load is a key's state from its checkpoint: snapshot (nil: none) decoded,
// then the journal chunks' records applied, oldest first, but for the first
// skip of them, which the snapshot holds. Every chunk's external inputs are
// applied in order, so each ends at its last value.
func Load(snapshot []byte, skip int, chunks ...[]byte) (*Loaded, error) {
	out := &Loaded{}
	var s *KeyState
	if snapshot != nil {
		v, err := decodeSnapshot(snapshot)
		if err != nil {
			return nil, err
		}
		s, out.Warm, out.Snapshot = v.State, v.Warm, true
	} else if skip != 0 {
		return nil, errors.New("state: records to skip with no snapshot")
	}
	if s == nil && len(chunks) == 0 {
		return nil, errors.New("state: no checkpoint to load")
	}
	var changes changeSet
	exts := map[string]bool{}
	for _, b := range chunks {
		c, err := decodeChunk(b)
		if err != nil {
			return nil, err
		}
		if s == nil {
			s = New(c.Key)
		}
		out.ChunkRecords = append(out.ChunkRecords, int64(len(c.Records)))
		for _, r := range c.Records {
			if skip > 0 {
				skip--
				continue
			}
			d := s.Apply(r)
			if out.Snapshot { // what the snapshot's warm state hasn't seen (none without one)
				changes.add(d.Changes)
			}
			out.Replayed++
		}
		for _, x := range c.Externals {
			s.setExt(x.ID, x.Value)
			exts[x.ID] = true
		}
	}
	if skip > 0 {
		return nil, fmt.Errorf("state: %d records the snapshot holds aren't in the journal", skip)
	}
	s.journal, s.extJournal = nil, nil
	out.State, out.Changes = s, changes.list()
	for id := range exts {
		out.Externals = append(out.Externals, id)
	}
	sort.Strings(out.Externals)
	return out, nil
}

// ChangeSet folds changes to each (table, id)'s net change: its first Old
// and its last Row.
type ChangeSet struct{ c changeSet }

// Add folds changes in.
func (c *ChangeSet) Add(cs []Change) { c.c.add(cs) }

// List is the net changes that change something, by (table, id).
func (c *ChangeSet) List() []Change { return c.c.list() }

// Journaled is how many records the journal holds since the last Encode: the
// next chunk's.
func (s *KeyState) Journaled() int { return len(s.journal) }
