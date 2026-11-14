package rollup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// The key commit objects (Commit, layout.go), read back. v5's rollup loop that
// wrote them run by run is gone (docs/PIPELINE.md v7, Deleted); WriteOnePass
// and the merge still write them for the oracle's layout (aintercept
// v1layout), until the final gate.

// newNonce names a merge run's output directory.
func newNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ReadNewest is key's newest commit (nil, nil: none yet). Superseded commits
// are deleted after the GC grace, so the listing stays short.
func ReadNewest(ctx context.Context, s stream.Store, prefix, key string) (*Commit, error) {
	objs, err := s.ListAfter(ctx, KeyDir(prefix, key)+"commit/", "")
	if err != nil {
		return nil, err
	}
	var best int64 = -1
	for _, o := range objs {
		if n, ok := CommitN(o.Key); ok && n > best {
			best = n
		}
	}
	if best < 0 {
		return nil, nil
	}
	b, err := s.Get(ctx, CommitKey(prefix, key, best))
	if err != nil {
		return nil, err
	}
	var c Commit
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("rollup: %s commit %d: %w", key, best, err)
	}
	return &c, nil
}

// ReadTables is key's current rows per table, as readers see them: every
// output dir of the newest commit, oldest first, newest row per (key, id).
func ReadTables(ctx context.Context, s stream.Store, prefix, key string) (map[string][]stream.Row, error) {
	c, err := ReadNewest(ctx, s, prefix, key)
	if err != nil || c == nil {
		return map[string][]stream.Row{}, err
	}
	sets := map[string][][]stream.Row{}
	for _, o := range c.Out {
		for _, table := range o.Tables {
			t, err := stream.TableFor(table)
			if err != nil {
				return nil, err
			}
			b, err := s.Get(ctx, KeyDir(prefix, key)+TableFile(o.Dir, table))
			if err != nil {
				return nil, err
			}
			rows, err := t.Decode(b)
			if err != nil {
				return nil, err
			}
			sets[table] = append(sets[table], rows)
		}
	}
	out := map[string][]stream.Row{}
	for table, ss := range sets {
		if rows := stream.MergeRows(ss...); len(rows) > 0 {
			out[table] = rows
		}
	}
	return out, nil
}
