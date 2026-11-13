package stream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// ErrFenced: another owner committed this bucket's next sequence first. The
// caller no longer owns the bucket and must drop what it was doing; its
// uncommitted files are never named by a commit, so readers never see them.
var ErrFenced = errors.New("stream: fenced by a newer commit")

// FileRef is one file a commit names, with the range of keys it holds (so a
// reader looking for one key opens only the files that can have it).
type FileRef struct {
	Key    string `json:"key"`
	MinKey string `json:"min_key"`
	MaxKey string `json:"max_key"`
	Rows   int    `json:"rows"`
	// Bytes is the file's size. Group (state files only) is the tier the file
	// is a key-ranged part of: a cycle's checkpoint run, or a fold of several.
	Bytes int64 `json:"bytes,omitempty"`
	Group int64 `json:"group,omitempty"`
	// Level and Seg place a base part in its tiered table base: parts of one
	// segment share Seg and never overlap; segments are ordered oldest first.
	Level int   `json:"level,omitempty"`
	Seg   int64 `json:"seg,omitempty"`
}

func (f FileRef) has(key string) bool { return f.MinKey <= key && key <= f.MaxKey }

// Garbage is a file a commit stopped naming, deleted once After passes, so a
// reader that loaded the previous commit can still finish reading it.
type Garbage struct {
	Key   string    `json:"key"`
	After time.Time `json:"after"`
}

// Commit is one entry of a bucket's commit log: everything a reader or the next
// owner needs. Exactly one writer creates each sequence number.
type Commit struct {
	Bucket  int       `json:"bucket"`
	Seq     int64     `json:"seq"`
	Epoch   int64     `json:"epoch"`
	Replica string    `json:"replica"`
	Nonce   string    `json:"nonce"`
	At      time.Time `json:"at"`
	// Watermark is the newest upload minute read; Processed names the record
	// files read in the grace window before it (older ones are never listed).
	Watermark string   `json:"watermark"`
	Processed []string `json:"processed"`
	// Each table's newest base generation (in key-ranged parts) and the run
	// files written since, oldest first. The base (Base and StateBase, which
	// grow with history) is written once per generation, to BaseManifest; a
	// commit names the manifest, not the base's parts, so every cycle writes a
	// commit the size of its runs. In memory, Base and StateBase are loaded.
	// Layout names the tables' shapes (the model's Layout): a reader knows
	// them from the commit, without opening a table file.
	Layout       string               `json:"layout,omitempty"`
	Gen          int64                `json:"gen"`
	BaseManifest string               `json:"base_manifest,omitempty"`
	Base         map[string][]FileRef `json:"base,omitempty"`
	Runs         map[string][]FileRef `json:"runs"`
	StateBase    []FileRef            `json:"state_base,omitempty"`
	State        []FileRef            `json:"state"`
	LastCompact  time.Time            `json:"last_compact"`
	Garbage      []Garbage            `json:"garbage"`
}

// BaseManifest is a base generation's parts: every table's, and the state's.
type BaseManifest struct {
	Gen       int64                `json:"gen"`
	Base      map[string][]FileRef `json:"base"`
	StateBase []FileRef            `json:"state_base"`
}

// Files are every file the commit names, garbage pending deletion included.
func (c *Commit) Files() []string {
	var out []string
	if c.BaseManifest != "" {
		out = append(out, c.BaseManifest)
	}
	for _, refs := range c.Base {
		for _, r := range refs {
			out = append(out, r.Key)
		}
	}
	for _, refs := range c.Runs {
		for _, r := range refs {
			out = append(out, r.Key)
		}
	}
	for _, r := range c.StateBase {
		out = append(out, r.Key)
	}
	for _, r := range c.State {
		out = append(out, r.Key)
	}
	for _, g := range c.Garbage {
		out = append(out, g.Key)
	}
	return out
}

// next is a copy of c as the start of its successor.
func (c *Commit) next(epoch int64, replica string, at time.Time) *Commit {
	n := &Commit{Bucket: c.Bucket, Seq: c.Seq + 1, Epoch: epoch, Replica: replica, Nonce: nonce(), At: at,
		Layout: c.Layout, Watermark: c.Watermark, Processed: append([]string(nil), c.Processed...), Gen: c.Gen, BaseManifest: c.BaseManifest,
		Base: map[string][]FileRef{}, Runs: map[string][]FileRef{},
		StateBase: append([]FileRef(nil), c.StateBase...), State: append([]FileRef(nil), c.State...),
		LastCompact: c.LastCompact, Garbage: append([]Garbage(nil), c.Garbage...)}
	for t, refs := range c.Base {
		n.Base[t] = append([]FileRef(nil), refs...)
	}
	for t, refs := range c.Runs {
		n.Runs[t] = append([]FileRef(nil), refs...)
	}
	return n
}

// nonce tells a writer's commit from another's when a create's outcome is
// ambiguous. A test making a golden layout replaces it with a counter.
var nonce = func() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// TryCommit creates c's sequence in the log. false: another writer created it
// first. On an ambiguous error (the write may have landed), it reads the log
// back and decides by the nonce.
func TryCommit(ctx context.Context, s Store, l Layout, c *Commit) (bool, error) {
	if c.Nonce == "" {
		c.Nonce = nonce()
	}
	if c.BaseManifest == "" && (len(c.Base) > 0 || len(c.StateBase) > 0) {
		// A new base generation: its parts go to a manifest of their own (a
		// name only this writer makes; a fenced writer's is collected).
		key := l.BaseManifestKey(c.Bucket, c.Gen, c.Epoch, c.Seq)
		mb, err := json.Marshal(BaseManifest{Gen: c.Gen, Base: c.Base, StateBase: c.StateBase})
		if err != nil {
			return false, err
		}
		if err := s.Put(ctx, key, mb); err != nil {
			return false, err
		}
		c.BaseManifest = key
	}
	wire := *c
	if wire.BaseManifest != "" {
		wire.Base, wire.StateBase = nil, nil
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return false, err
	}
	ok, err := s.PutIfAbsent(ctx, l.CommitKey(c.Bucket, c.Seq), b)
	if err == nil {
		return ok, nil
	}
	got, gerr := s.Get(ctx, l.CommitKey(c.Bucket, c.Seq))
	if gerr != nil {
		if errors.Is(gerr, process.ErrNotFound) {
			return false, err // it didn't land: the caller retries
		}
		return false, err
	}
	var won Commit
	if json.Unmarshal(got, &won) == nil && won.Nonce == c.Nonce {
		return true, nil
	}
	return false, nil
}

// LoadLatest is a bucket's newest commit, nil if it has none.
func LoadLatest(ctx context.Context, s Store, l Layout, b int) (*Commit, error) {
	return LoadLatestAfter(ctx, s, l, b, 0)
}

// LoadLatestAfter is LoadLatest listing only commits after seq (one the caller
// already knows): the log is long-lived, so readers don't list all of it.
func LoadLatestAfter(ctx context.Context, s Store, l Layout, b int, seq int64) (*Commit, error) {
	after := ""
	if seq > 0 {
		after = l.CommitKey(b, seq)
	}
	objs, err := s.ListAfter(ctx, l.CommitPrefix(b), after)
	if err != nil {
		return nil, err
	}
	for i := len(objs) - 1; i >= 0; i-- {
		if _, ok := commitSeq(objs[i].Key); !ok {
			continue
		}
		raw, err := s.Get(ctx, objs[i].Key)
		if errors.Is(err, process.ErrNotFound) {
			continue // collected while listing
		}
		if err != nil {
			return nil, err
		}
		var c Commit
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("commit %s: %w", objs[i].Key, err)
		}
		return &c, loadBase(ctx, s, &c)
	}
	if seq > 0 { // nothing newer: the known one is still the latest
		raw, err := s.Get(ctx, l.CommitKey(b, seq))
		if err != nil {
			return nil, err
		}
		var c Commit
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		return &c, loadBase(ctx, s, &c)
	}
	return nil, nil
}

// loadBase reads the commit's base generation from its manifest.
func loadBase(ctx context.Context, s Store, c *Commit) error {
	if c.Runs == nil {
		c.Runs = map[string][]FileRef{}
	}
	if c.BaseManifest == "" {
		if c.Base == nil {
			c.Base = map[string][]FileRef{}
		}
		return nil
	}
	raw, err := s.Get(ctx, c.BaseManifest)
	if err != nil {
		return fmt.Errorf("base manifest %s: %w", c.BaseManifest, err)
	}
	var m BaseManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("base manifest %s: %w", c.BaseManifest, err)
	}
	c.Base, c.StateBase = m.Base, m.StateBase
	if c.Base == nil {
		c.Base = map[string][]FileRef{}
	}
	return nil
}

func commitSeq(key string) (int64, bool) {
	name := key[strings.LastIndex(key, "/")+1:]
	name, ok := strings.CutPrefix(name, "commit-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
	return n, err == nil
}
