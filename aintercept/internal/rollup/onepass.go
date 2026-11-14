package rollup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// WriteOnePass writes a key's tables as its first commit: one output directory
// holding every table it has rows in. It's how tools and tests lay out a
// capture's one-pass result (`aintercept v1layout`); the rollup loop writes
// the same layout run by run. newestRowNS is the newest raw row the key's
// ingest files carried, rows that complete no record included (a call still in
// flight), as the loop's commits record it (Commit.NewestRowNS); 0 for none.
func WriteOnePass(ctx context.Context, s stream.Store, prefix, key string, tables map[string][]stream.Row, updatedNS, newestRowNS int64) error {
	names := make([]string, 0, len(tables))
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	dir := OutDir(1, "onepass")
	out := Out{Dir: dir}
	for _, t := range names {
		tab, err := stream.TableFor(t)
		if err != nil {
			return err
		}
		b, err := tab.Encode(tables[t])
		if err != nil {
			return err
		}
		if err := s.Put(ctx, KeyDir(prefix, key)+TableFile(dir, t), b); err != nil {
			return err
		}
		out.Tables = append(out.Tables, t)
	}
	if err := markReported(ctx, s, prefix, key, tables[state.TableRequestMissed]); err != nil {
		return err
	}
	body, err := json.Marshal(Commit{N: 1, Key: key, Out: []Out{out}, UpdatedNS: updatedNS, NewestRowNS: newestRowNS})
	if err != nil {
		return err
	}
	ok, err := s.PutIfAbsent(ctx, CommitKey(prefix, key, 1), body)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("rollup: %s already has a commit", key)
	}
	return nil
}

// KeysQueue is a fixed set of committed keys, as Redis would hold them: what a
// tool's one merge run takes.
type KeysQueue struct{ Keys []string }

func (q *KeysQueue) Pop(_ context.Context, n int) ([]string, error) {
	if n > len(q.Keys) {
		n = len(q.Keys)
	}
	out := q.Keys[:n]
	q.Keys = q.Keys[n:]
	return out, nil
}
func (q *KeysQueue) Requeue(_ context.Context, keys []string) error {
	q.Keys = append(q.Keys, keys...)
	return nil
}
func (q *KeysQueue) Alive(context.Context) (bool, error) { return true, nil }
func (q *KeysQueue) MarkAlive(context.Context) error     { return nil }
