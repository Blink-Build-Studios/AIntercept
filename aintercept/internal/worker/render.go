package worker

import (
	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The rendered tables' seam (docs/PIPELINE.md §2 Run 6; internal/render is build step 3's). A run
// calls Render once its records are applied and its calls expired, before the
// commit. The commit writes the returned changes in the run's transaction,
// after the guard and the stored inputs (pg.QueueChanges: one unnest upsert
// and one delete per table), and records RenderVersion in keys.render_version.
// The run keeps Baseline with its warm state and hands it back next run; a
// failed or lost run drops both, so the next Render gets nil (a cold load).

// Renderer renders a key's rows for the commit.
type Renderer interface {
	Render(in RenderInput) (RenderOutput, error)
	// Baseline is the baseline of the rows committed with a state just loaded
	// cold (before this run applies anything), day its key row's: what Render
	// gets as RenderInput.Baseline. Postgres is never read for it.
	Baseline(key string, st *state.KeyState, day string) (any, error)
}

// RenderInput is one run's state for the render.
type RenderInput struct {
	Key string
	// State is the key's state after this run (read only).
	State *state.KeyState
	// Prev is the key row the run read: what's committed. Prev.N 0 is a new
	// key; Prev.RenderVersion is the version of the rows in Postgres.
	Prev KeyRow
	// Baseline is what the previous Render on this warm state returned, or
	// after a cold load Renderer.Baseline's (nil for a new key).
	Baseline any
	// Day is the keys.day this commit writes (session_rows.key_day).
	Day string
	// Changed is the state's rows this run changed (its deltas), by table and
	// id: the drawer derives again only the rows they can have changed since
	// Baseline's render (§3.7).
	Changed drawer.Changed
}

// RenderOutput is what the commit writes for the render.
type RenderOutput struct {
	// Changes are rendered tables' upserts and deletes (§3.3, §3.5), in each
	// table's column order (pg.Table.Columns, extras included), and Queue
	// queues statements of its own; both go in the commit's batch after the
	// stored inputs.
	Changes []pg.Changes
	Queue   func(*pgx.Batch)
	// Baseline is kept with the warm state once the commit lands.
	Baseline any
	// RenderVersion is recorded in keys.render_version (0: keep Prev's).
	RenderVersion int
	// ChangedBatches are the batches whose batch_parts row the commit writes or
	// deletes: the other keys holding them are flagged (§3.7).
	ChangedBatches []string
}

// NoRender renders nothing.
type NoRender struct{}

func (NoRender) Render(RenderInput) (RenderOutput, error)              { return RenderOutput{}, nil }
func (NoRender) Baseline(string, *state.KeyState, string) (any, error) { return nil, nil }

// Tables is the worker's renderer: internal/render's rows (a session's 9
// tables, its call days, every key's agent calls and batch parts), written as
// the diff from the baseline, or whole when the key's rows were written by
// another render version.
type Tables struct{}

func (Tables) Render(in RenderInput) (RenderOutput, error) {
	base, _ := in.Baseline.(*render.Baseline)
	rows, err := render.RenderNext(in.Key, in.State.Rows(), in.State.Externals(), in.Day, base, in.Changed,
		in.State.ExternalsChanged())
	if err != nil {
		return RenderOutput{}, err
	}
	var d *render.Diff
	if in.Prev.N > 0 && in.Prev.RenderVersion != render.Version {
		d = rows.Rewrite()
	} else {
		d = rows.Diff(base)
	}
	return RenderOutput{Queue: d.Queue, Baseline: rows.Baseline(), RenderVersion: render.Version,
		ChangedBatches: d.Changed("batch_parts")}, nil
}

func (Tables) Baseline(key string, st *state.KeyState, day string) (any, error) {
	rows, err := render.RenderState(key, st, day)
	if err != nil {
		return nil, err
	}
	return rows.Baseline(), nil
}
