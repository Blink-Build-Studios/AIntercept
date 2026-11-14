package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// KeyRow is a key's row in keys (§3.2): v5's commit JSON, plus what the merge
// used to fold, computed by the key's run. N 0 is a key never committed.
type KeyRow struct {
	Key         string
	N           int64
	State       []string // the state chunks, oldest first (relative to the key's folder)
	StateLevels []int    // each chunk's compaction tier
	LastApplied string   // the newest in/ name applied
	Grace       []string // in/ names applied within the grace window
	Garbage     []rollup.Garbage
	// RenderVersion is the version of the render that wrote the key's rendered rows.
	RenderVersion int
	UpdatedNS     int64
	// NewestRowNS is the newest raw row time its files carried (or its fact
	// rows hold): data time is the largest over all keys.
	NewestRowNS int64
	// ExpiresNS is when its first call still waiting for its ending expires
	// (0: none waits): the due calls flag it once data time passes.
	ExpiresNS       int64
	Day             string // rollup.DayOf's
	FirstRequestNS  int64
	EarliestNS      int64
	ContentRetained bool
	// Render is the key's flag's render (dirty_keys.render, not a keys column):
	// the run renders and commits even with no new file (§3.7).
	Render bool
}

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const keyColumns = `coalesce(k.n, 0), coalesce(k.state, '{}'), coalesce(k.state_levels, '{}'), coalesce(k.last_applied, ''),
	coalesce(k.grace, '{}'), coalesce(k.garbage, '[]'), coalesce(k.render_version, 0), coalesce(k.updated_ns, 0),
	coalesce(k.newest_row_ns, 0), coalesce(k.expires_ns, 0), coalesce(k.day, ''), coalesce(k.first_request_ns, 0),
	coalesce(k.earliest_ns, 0), coalesce(k.content_retained, false)`

// readRow is key's row and its flag's seq in one round (§2 Run 1): with no
// row, N is 0; with no flag, seq is 0.
func readRow(ctx context.Context, q Querier, key string) (KeyRow, int64, error) {
	row := KeyRow{Key: key}
	var seq int64
	var levels []int32
	var garbage []byte
	err := q.QueryRow(ctx, `SELECT coalesce(d.seq, 0), coalesce(d.render, false), `+keyColumns+`
		FROM (SELECT $1::text AS key) q LEFT JOIN keys k ON k.key = q.key LEFT JOIN dirty_keys d ON d.key = q.key`, key).Scan(
		&seq, &row.Render, &row.N, &row.State, &levels, &row.LastApplied, &row.Grace, &garbage, &row.RenderVersion, &row.UpdatedNS,
		&row.NewestRowNS, &row.ExpiresNS, &row.Day, &row.FirstRequestNS, &row.EarliestNS, &row.ContentRetained)
	if err != nil {
		return row, 0, err
	}
	for _, l := range levels {
		row.StateLevels = append(row.StateLevels, int(l))
	}
	if err := json.Unmarshal(garbage, &row.Garbage); err != nil {
		return row, 0, fmt.Errorf("worker: %s garbage: %w", key, err)
	}
	return row, seq, nil
}

// guard writes next as the key's row, guarded on prev's n (§2 Run 8.1): an
// UPDATE … WHERE n = prev.N, or a new key's INSERT … ON CONFLICT DO NOTHING.
// It takes the key row's lock first, so concurrent commits serialize there;
// false is another worker's commit (nothing was written).
func guard(ctx context.Context, tx pgx.Tx, prev, next KeyRow) (bool, error) {
	garbage, err := json.Marshal(next.Garbage)
	if err != nil {
		return false, err
	}
	if next.Garbage == nil {
		garbage = []byte("[]")
	}
	levels := make([]int32, len(next.StateLevels))
	for i, l := range next.StateLevels {
		levels[i] = int32(l)
	}
	args := []any{next.Key, prev.N, next.State, levels, next.LastApplied, next.Grace, string(garbage), next.RenderVersion,
		next.UpdatedNS, next.NewestRowNS, next.ExpiresNS, next.Day, next.FirstRequestNS, next.EarliestNS, next.ContentRetained}
	var sql string
	if prev.N == 0 {
		sql = `INSERT INTO keys (key, n, state, state_levels, last_applied, grace, garbage, render_version, updated_ns,
			newest_row_ns, expires_ns, day, first_request_ns, earliest_ns, content_retained)
			VALUES ($1, $2 + 1, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT (key) DO NOTHING`
	} else {
		sql = `UPDATE keys SET n = n + 1, state = $3, state_levels = $4, last_applied = $5, grace = $6, garbage = $7::jsonb,
			render_version = $8, updated_ns = $9, newest_row_ns = $10, expires_ns = $11, day = $12, first_request_ns = $13,
			earliest_ns = $14, content_retained = $15 WHERE key = $1 AND n = $2`
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// dataTime is data time (§2, calls that never end): the newest raw row any
// key has seen, through the newest_row_ns index.
func dataTime(ctx context.Context, q Querier) (int64, error) {
	var t int64
	err := q.QueryRow(ctx, "SELECT coalesce(max(newest_row_ns), 0) FROM keys").Scan(&t)
	return t, err
}

// jsonGarbage parses a key row's garbage column into row.
func jsonGarbage(b []byte, row *KeyRow) error {
	if err := json.Unmarshal(b, &row.Garbage); err != nil {
		return fmt.Errorf("worker: %s garbage: %w", row.Key, err)
	}
	return nil
}
