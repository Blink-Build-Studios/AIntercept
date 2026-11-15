package worker

import (
	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The rendered tables' seam (docs/PIPELINE.md §2 Run 6). A run calls Render
// once its records are applied and its calls expired, before the commit. The
// commit writes the returned changes in the run's transaction, after the
// guard and the stored inputs (pg.QueueChanges: one unnest upsert and one
// delete per table), and records RenderVersion in keys.render_version.
//
// The run keeps Warm with its warm state and hands it back next run, and
// writes it in the key's snapshot (v8: a cold load decodes it, and nothing
// renders the key to load it). A failed or lost run drops both.

// Renderer renders a key's rows for the commit.
type Renderer interface {
	Render(in RenderInput) (RenderOutput, error)
}

// RenderInput is one run's state for the render.
type RenderInput struct {
	Key string
	// State is the key's state after this run (read only).
	State *state.KeyState
	// Prev is the key row the run read: what's committed. Prev.N 0 is a new
	// key; Prev.RenderVersion is the version of the rows in Postgres.
	Prev KeyRow
	// Warm is what the previous Render on this warm state returned, or what
	// a cold load decoded from the key's snapshot (caught up to the key's
	// last commit); nil with none (a new key, or no snapshot it can read):
	// the render renders every row.
	Warm any
	// Day is the keys.day this commit writes (session_rows.key_day).
	Day string
	// Changes are the state's changes since Warm's render, each (table,
	// id)'s first Old and last Row: this run's records'. (A cold load's
	// catch-up render is given the journal's records after the snapshot, and
	// its output is only its Warm: their rows are committed.)
	Changes []state.Change
	// ExtChanged are the external inputs set since Warm's render.
	ExtChanged []string
}

// RenderOutput is what the commit writes for the render.
type RenderOutput struct {
	// Changes are rendered tables' upserts and deletes (§3.3, §3.5), in each
	// table's column order (pg.Table.Columns, extras included), and Queue
	// queues statements of its own; both go in the commit's batch after the
	// stored inputs.
	Changes []pg.Changes
	Queue   func(*pgx.Batch)
	// Warm is kept with the warm state once the commit lands, and written in
	// the key's snapshot.
	Warm any
	// RenderVersion is recorded in keys.render_version (0: keep Prev's).
	RenderVersion int
	// ChangedBatches are the batches whose batch_parts row the commit writes or
	// deletes: the other keys holding them are flagged (§3.7).
	ChangedBatches []string
}

// NoRender renders nothing.
type NoRender struct{}

func (NoRender) Render(RenderInput) (RenderOutput, error) { return RenderOutput{}, nil }

// Tables is the worker's renderer: internal/render's rows (a session's
// tables, its call days, every key's agent calls, batch parts and drawer),
// written as what changed since the warm state, or whole when there's none
// or the key's rows were written by another render version.
type Tables struct{}

func (Tables) Render(in RenderInput) (RenderOutput, error) {
	warm, _ := in.Warm.(*render.Warm)
	if in.Prev.N > 0 && in.Prev.RenderVersion != render.Version {
		warm = nil // another render's rows: rewritten whole
	}
	rows, err := render.RenderNext(in.Key, in.State.Rows(), in.State.Externals(), in.Day, warm, in.Changes, in.ExtChanged)
	if err != nil {
		return RenderOutput{}, err
	}
	var d *render.Diff
	if in.Prev.N > 0 && warm == nil {
		d = rows.Rewrite() // rows in Postgres no warm state describes
	} else {
		d = rows.Diff(warm)
	}
	return RenderOutput{Queue: d.Queue, Warm: rows.Warm(), RenderVersion: render.Version,
		ChangedBatches: d.Changed("batch_parts")}, nil
}
