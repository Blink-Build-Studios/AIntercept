package worker

import (
	"fmt"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// v8 step 2 (docs/PIPELINE.md §2, v8's tests): the running values, the dirty
// inputs, and snapshots, on the worker's layers.

// A render flag on a key with no state (a batch's other key that hasn't
// committed yet) is consumed without writing a key row: a key row is only
// ever written with a real day.
func TestARenderFlagOnAKeyWithNoStateWritesNoKeyRow(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("nobody")
	e.renderFlag(key, "b1")
	if res := e.run(e.worker("w1"), key); res.Committed {
		t.Fatalf("a commit for a key with no state: %+v", res)
	}
	var keys, inputs int
	if err := e.db.QueryRow(e.ctx, "SELECT (SELECT count(*) FROM keys), (SELECT count(*) FROM dirty_inputs)").Scan(&keys, &inputs); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || inputs != 0 || e.flags() != 0 {
		t.Fatalf("keys %d, dirty inputs %d, flags %d: want none of each", keys, inputs, e.flags())
	}
}

// dirty_inputs: a batch's other key, flagged by a commit that changed the
// batch's part, re-reads only that batch's parts, never every batch it holds;
// and its commit consumes what it read.
func TestDirtyInputsNameWhatTheReaderReads(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock.Now().UnixNano()
	ka, kb := sessionKey("sa"), sessionKey("sb")
	// A holds two batches' calls and events; B holds a part of each.
	e.putFile([]records.Record{e.batchEvent("sa", "b1", 1, t0+1), e.batchCall("b1", "sa", t0+2),
		e.batchEvent("sa", "b2", 1, t0+3), e.batchCall("b2", "sa", t0+4)}, 0, e.clock.Now())
	w := e.worker("w1")
	if res := e.run(w, ka); !reflect.DeepEqual(res.PartsRead, []string{"b1", "b2"}) {
		t.Fatalf("A's first run read %v, want every batch it holds", res.PartsRead)
	}
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.batchEvent("sb", "b1", 2, t0+5), e.batchEvent("sb", "b2", 2, t0+6)}, 0, e.clock.Now())
	e.drain(w)
	e.check()
	// B's part of b2 changes: A is flagged with b2 alone.
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.batchEvent("sb", "b2", 3, t0+7)}, 0, e.clock.Now())
	e.run(w, kb)
	row := e.keyRow(ka)
	if !row.Render || !reflect.DeepEqual(row.Inputs, []DirtyInput{{inputParts, "b2", 1}}) {
		t.Fatalf("A's flag: render %v, inputs %+v; want b2's", row.Render, row.Inputs)
	}
	res := e.run(w, ka)
	if !res.Committed || !reflect.DeepEqual(res.PartsRead, []string{"b2"}) {
		t.Fatalf("A's run %+v: want a commit that read b2's parts alone", res)
	}
	if e.keyRow(ka).Inputs != nil {
		t.Fatal("A's commit left its dirty inputs")
	}
	e.drain(w)
	e.check()
}

// A dirty input written again while its reader runs (another commit's) has a
// new seq: the run consumes the one it read, and the new one stays for the
// next run.
func TestADirtyInputWrittenMidRunSurvives(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock.Now().UnixNano()
	ka := sessionKey("sa")
	e.putFile([]records.Record{e.batchEvent("sa", "b1", 1, t0+1), e.batchCall("b1", "sa", t0+2)}, 0, e.clock.Now())
	w0 := e.worker("w0")
	e.drain(w0) // w0 holds the key's lock from here on
	e.renderFlag(ka, "b1")
	w := e.worker("w1", withHook(func(step, key string) error {
		if step == "read" && key == ka {
			e.renderFlag(ka, "b1") // another commit's, while the run reads
		}
		return nil
	}))
	e.run(w, ka)
	if in := e.keyRow(ka).Inputs; !reflect.DeepEqual(in, []DirtyInput{{inputParts, "b1", 2}}) {
		t.Fatalf("inputs after the run: %+v; want b1's newer one", in)
	}
	e.drain(w0)
	if e.keyRow(ka).Inputs != nil {
		t.Fatal("the next run left the input")
	}
	e.check()
}

// Snapshots (v8): a key's first commit writes one, and a run whose journal
// holds as many records since its last as it does; going cold (idle, evicted,
// SIGTERM) writes one when the journal holds a quarter of its records or more
// since; and a cold load decodes it and applies only the journal's records
// after it, never the key's history, and stays exact.
func TestColdLoadsDecodeTheSnapshot(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	w := e.worker("w1")
	e.ingest("a", 8)
	e.run(w, key)
	if row := e.keyRow(key); row.Snapshot == "" || row.SnapshotRecords == 0 {
		t.Fatalf("the first commit wrote no snapshot: %+v", row)
	}
	// A few small runs: their records are the journal's tail, after the snapshot.
	for i := 0; i < 3; i++ {
		e.clock.Advance(time.Second)
		e.ingest("a", 1)
		e.run(w, key)
	}
	row := e.keyRow(key)
	held := row.SnapshotRecords
	// A crash (no snapshot on going cold): the next holder decodes the snapshot
	// and applies only the tail.
	w.Kill()
	w2 := e.worker("w2")
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	res := e.run(w2, key)
	if !res.Cold || !res.Decoded || res.Replayed == 0 || int64(res.Replayed) >= held {
		t.Fatalf("after a crash: %+v; want a decode and the tail (< %d records) applied", res, held)
	}
	e.check()
	// Going cold on SIGTERM with a quarter of its records or more since its
	// snapshot writes one: the next holder applies nothing.
	e.clock.Advance(time.Second)
	e.ingest("a", quarterTail(e.keyRow(key)))
	e.drain(w2) // claims the key: held, and warm after its commit
	if row := e.keyRow(key); !w2.Warm(key) || row.SnapshotRecords == sum(row.StateRecords) {
		t.Fatal("want a warm key whose last commit wrote no snapshot")
	}
	w2.Release(e.ctx)
	if row := e.keyRow(key); row.SnapshotRecords != sum(row.StateRecords) {
		t.Fatalf("SIGTERM's snapshot holds %d of %d records", row.SnapshotRecords, sum(row.StateRecords))
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	if res := e.run(e.worker("w3"), key); !res.Decoded || res.Replayed != 0 {
		t.Fatalf("after SIGTERM: %+v; want a decode and nothing applied", res)
	}
	e.check()
	// Idle past the window with a small tail: unlocked with no snapshot (the
	// tail costs less to apply than a snapshot to write), and the next load
	// applies that tail alone.
	w4 := e.worker("w4", func(c *Config) { c.Idle = time.Minute })
	idle := func(n int) {
		e.clock.Advance(time.Second)
		e.ingest("a", n)
		e.drain(w4)
		e.clock.Advance(2 * time.Minute)
		e.ingest("b", 1) // another key's file: a tick that releases the idle one
		e.drain(w4)
		if w4.Warm(key) {
			t.Fatal("the idle key stayed warm")
		}
	}
	idle(1)
	row = e.keyRow(key)
	if tail := sum(row.StateRecords) - row.SnapshotRecords; tail == 0 || 4*tail >= sum(row.StateRecords) {
		t.Fatalf("a small tail: %d of %d records", tail, sum(row.StateRecords))
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	if res := e.run(e.worker("w5"), key); !res.Decoded || 4*res.Replayed >= int(row.SnapshotRecords) {
		t.Fatalf("after going idle: %+v; want a decode and the small tail applied", res)
	}
	e.check()
	// With a quarter of its records or more since its snapshot, going idle writes one.
	idle(quarterTail(e.keyRow(key)))
	if row := e.keyRow(key); row.SnapshotRecords != sum(row.StateRecords) {
		t.Fatalf("going idle wrote no snapshot: %d of %d records", row.SnapshotRecords, sum(row.StateRecords))
	}
	e.check()
}

// quarterTail is how many records to add to a key so its journal holds a
// quarter of its records or more since its snapshot, but fewer than the
// snapshot does (no run writes another).
func quarterTail(row KeyRow) int {
	snap, tail := row.SnapshotRecords, sum(row.StateRecords)-row.SnapshotRecords
	return max(int(snap/2-tail), 1)
}

// renderFlag flags key to render with a batch's parts as its dirty input, as
// another key's commit does.
func (e *env) renderFlag(key, batch string) {
	e.t.Helper()
	if _, err := e.db.Exec(e.ctx, `INSERT INTO dirty_keys (key, seq, since, render) VALUES ($1, 1, now(), true)
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1, render = true`, key); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Exec(e.ctx, `INSERT INTO dirty_inputs (key, kind, id, seq) VALUES ($1, 'parts', $2, 1)
		ON CONFLICT (key, kind, id) DO UPDATE SET seq = dirty_inputs.seq + 1`, key, batch); err != nil {
		e.t.Fatal(err)
	}
}

func sum(xs []int64) int64 {
	var n int64
	for _, x := range xs {
		n += x
	}
	return n
}

// A snapshot a later build can't read (its types changed) is never the only
// copy: the key loads from its journal, renders every row, and stays exact.
func TestAStaleSnapshotLoadsFromTheJournal(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	w := e.worker("w1")
	e.ingest("a", 6)
	e.run(w, key)
	e.clock.Advance(time.Second)
	e.ingest("a", 2)
	e.run(w, key)
	w.Kill()
	row := e.keyRow(key)
	p := rollup.KeyDir(prefix, key) + row.Snapshot
	b, err := e.store.Get(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsSnapshot(b) {
		t.Fatal("not a snapshot")
	}
	b[len("AISNAP1\n")] ^= 0xff // another build's fingerprint
	if err := e.store.Put(e.ctx, p, b); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	res := e.run(e.worker("w2"), key)
	if !res.Cold || res.Decoded || int64(res.Replayed) != sum(row.StateRecords) {
		t.Fatalf("%+v: want the journal's %d records replayed", res, sum(row.StateRecords))
	}
	e.check()
}

// A cold load after a crash has a journal tail after its snapshot: rows its
// commits wrote that the snapshot's warm state doesn't know. Its render
// catches up to them before the run's own records, so a row the tail wrote
// and the run removes is deleted. Crashes every few runs, each checked.
func TestColdLoadsAfterCrashesCatchUp(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	w := e.worker("w0")
	for i := 0; i < 24; i++ {
		e.clock.Advance(time.Second)
		e.ingest("a", 1+i%3)
		if i%4 == 3 {
			w.Kill()
			w = e.worker(fmt.Sprintf("w%d", i))
		}
		res := e.run(w, key)
		if i%4 == 3 && (!res.Cold || !res.Decoded) {
			t.Fatalf("run %d after a crash: %+v", i, res)
		}
		e.check()
	}
}

// The dimensions' rule (§3.11: the changed rows): a run writes each changed
// stored row, by (key, its state id), an upsert or a delete, and nothing
// else; a state table that isn't stored writes nothing.
func TestTheDimensionsWriteTheChangedRows(t *testing.T) {
	got := StoredChanges("s:a", map[string][]stream.Row{
		state.TablePersonDevices: {{ID: "pd:1", Value: state.PersonDevice{PersonID: "acct-1", DeviceID: "dev-1"}},
			{ID: "pd:2", Tomb: true}},
		state.TableEvents: {{ID: "e:1", Value: state.Event{}}}, // rendered, not stored
	})
	if len(got) != 1 || got[0].Table != "dim_person_device" || len(got[0].Upserts) != 1 || len(got[0].Deletes) != 1 {
		t.Fatalf("stored changes %+v", got)
	}
	if u, d := got[0].Upserts[0], got[0].Deletes[0]; u[0] != "s:a" || string(u[1].([]byte)) != "pd:1" ||
		d[0] != "s:a" || string(d[1].([]byte)) != "pd:2" {
		t.Fatalf("upsert %v, delete %v", u, d)
	}
}

// The holder's side of the cross-key rule (§2): a key holding only a batch's
// call (a credential's key) reads the other keys' parts when its call row
// lands, and nothing re-reads them later unless something names them. In
// both commit orders it ends with the batch's parts: the parts' writer flags
// the call's holder, or the holder's commit, under the batch's lock, sees a
// part its render didn't read and flags itself.
func TestTheCallsHolderSeesTheBatchsPartsInBothOrders(t *testing.T) {
	for _, holderFirst := range []bool{true, false} {
		e := newEnv(t)
		t0 := e.clock.Now().UnixNano()
		call := e.batchCall("bh", "", t0+5)
		holder := call.RoutingKey
		events := []records.Record{e.batchEvent("sa", "bh", 1, t0+1), e.batchEvent("sa", "bh", 2, t0+2)}
		ka := events[0].RoutingKey
		if holder == ka || !strings.HasPrefix(holder, "c:") {
			t.Fatalf("the call's key %s, the events' %s", holder, ka)
		}
		e.putFile(append([]records.Record{call}, events...), 0, e.clock.Now())
		w := e.worker("w1")
		order := []string{ka, holder}
		if holderFirst {
			order = []string{holder, ka}
		}
		for _, k := range order {
			e.run(w, k)
		}
		e.drain(w)
		v, ok := e.stateOf(holder).External(render.PartsID("bh"))
		if p, _ := v.(state.OtherParts); !ok || p.N != 2 || p.FirstSession != "sa" {
			t.Fatalf("holder first %v: the holder's parts of the batch %+v (%v)", holderFirst, v, ok)
		}
		e.check()
	}
}

// stateOf is key's state as its checkpoint holds it.
func (e *env) stateOf(key string) *state.KeyState {
	e.t.Helper()
	row := e.keyRow(key)
	l, err := (&Worker{cfg: e.cfg("probe")}).load(e.ctx, key, &row)
	if err != nil {
		e.t.Fatal(err)
	}
	return l.State
}
