package worker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// The tail as a singleton, the render seam (build step 3), and the key row's
// folded facts.

// The tail flags the keys ingest's index names, idle ones included, and every
// run consumes its flag: dirty_keys ends empty and /lag at 0.
func TestTailFlagsAndRunsConsume(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	if _, err := e.db.Exec(e.ctx, "DELETE FROM dirty_keys"); err != nil { // ingest never flags: the tail does
		t.Fatal(err)
	}
	name := events.ResolveUploadKey(keyflag.Template(prefix, "pod-1", 1), e.clock.Now())
	body, _ := json.Marshal(keyflag.Object{Incarnation: "pod-1", Keys: []string{key, sessionKey("idle")}, LastNS: 1})
	if err := e.store.Put(e.ctx, name, body); err != nil {
		t.Fatal(err)
	}
	w := e.worker("w1", func(c *Config) {
		c.Singletons = []Singleton{TailSingleton(tail.New(tail.Config{DB: e.db, Store: e.store, Prefix: prefix}))}
	})
	if err := w.Tick(e.ctx); err != nil { // the tail runs after this tick's runs
		t.Fatal(err)
	}
	if n := e.flags(); n != 2 {
		t.Fatalf("the tail flagged %d keys, want 2", n)
	}
	if !contains(w.Singletons(), "tail") {
		t.Fatal("w1 doesn't hold the tail")
	}
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.flags(); n != 0 {
		t.Fatalf("%d flags left", n)
	}
	if lag, err := w.Lag(e.ctx); err != nil || lag > time.Second {
		t.Fatalf("lag %v %v", lag, err)
	}
	e.check()
}

// recRender is a renderer that writes one session_rows row per run (its
// events count) and counts the runs it saw with and without a baseline.
type recRender struct {
	warm, cold int
	fail       bool
}

func (r *recRender) Baseline(string, *state.KeyState, string) (any, error) { return nil, nil }

func (r *recRender) Render(in RenderInput) (RenderOutput, error) {
	if r.fail {
		return RenderOutput{}, errors.New("render failed")
	}
	if in.Baseline == nil {
		r.cold++
	} else {
		r.warm++
	}
	tb := pg.TableNamed("session_rows")
	row := make([]any, len(tb.Columns))
	for i, c := range tb.Columns {
		switch c.Name {
		case "session_id":
			row[i] = in.Key
		case "events":
			row[i] = int64(len(in.State.Rows()["fact_events"]))
		}
	}
	return RenderOutput{Changes: []pg.Changes{{Table: "session_rows", Upserts: [][]any{row}}}, Baseline: in.Prev.N + 1, RenderVersion: 7}, nil
}

func TestRenderSeam(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	r := &recRender{}
	w := e.worker("w1", func(c *Config) { c.Render = r })
	events := func() int64 {
		var n int64
		if err := e.db.QueryRow(e.ctx, "SELECT events FROM session_rows WHERE session_id = $1", key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	e.ingest("a", 4) // 3 of the mix's 4 are events
	e.drain(w)
	if events() != 3 || e.keyRow(key).RenderVersion != 7 {
		t.Fatalf("rendered %d events, version %d", events(), e.keyRow(key).RenderVersion)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 4)
	e.drain(w)
	if events() != 6 || r.cold != 1 || r.warm != 1 {
		t.Fatalf("events %d; renders cold %d warm %d (want 6, 1, 1)", events(), r.cold, r.warm)
	}
	// A failed render commits nothing: no key row, no stored inputs, no rendered rows.
	r.fail = true
	e.clock.Advance(time.Second)
	e.ingest("a", 4)
	_ = w.Tick(e.ctx)
	if events() != 6 || e.keyRow(key).N != 2 || e.flagOf(key).failures != 1 {
		t.Fatalf("after a failed render: events %d, n %d, flag %+v", events(), e.keyRow(key).N, e.flagOf(key))
	}
	// The warm state went with it: the next render starts cold.
	r.fail = false
	e.drain(w)
	if events() != 9 || r.cold != 2 {
		t.Fatalf("events %d, cold renders %d", events(), r.cold)
	}
	e.storedOnly = true
	e.check()
}

// The key row keeps what the merge folded: day, /status's first request and
// earliest row, whether content is retained.
func TestKeyRowFacts(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	t0 := e.clock.Now().UnixNano()
	c := ccCall("c1", "a", t0+5)
	c.DetailJSON = `{"x":1}`
	probe := ccCall("p1", "a", t0+1)
	probe.Synthetic = true
	e.putFile([]records.Record{e.end(c, t0+10), e.end(probe, t0+2), e.event("a", "tengu_x", t0+20, "")}, 0, e.clock.Now())
	e.drain(e.worker("w1"))
	row := e.keyRow(key)
	if row.Day != "2026-11-13" || row.FirstRequestNS != t0+5 || row.EarliestNS != t0+1 || !row.ContentRetained {
		t.Fatalf("key row %+v", row)
	}
	e.check()
}

// A key whose rendered rows another render version wrote has them all
// rewritten by its next commit; a cold load renders its baseline from the
// state, and stays exact.
func TestRenderVersionRewritesTheSession(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 6)
	w := e.worker("w1")
	e.drain(w)
	e.check()
	// An older build's rows: a stray row, a wrong one, and the old version.
	if _, err := e.db.Exec(e.ctx, `UPDATE event_rows SET name = 'stale' WHERE session_id = 'a';
		INSERT INTO prompt_steps (session_id, seq, step) VALUES ('a', 99, 1);
		UPDATE keys SET render_version = 0`); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 2)
	w.Release(e.ctx)
	w2 := e.worker("w2") // a cold load
	e.drain(w2)
	if v := e.keyRow(key).RenderVersion; v != render.Version {
		t.Fatalf("render_version %d", v)
	}
	e.check()
	// A cold load at this version writes only the new rows.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	w2.Release(e.ctx)
	e.drain(e.worker("w3"))
	e.check()
}
