package worker

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// Retention (docs/PIPELINE.md §3.6): every hour, the keys whose day is older
// than the retention window are forgotten, whole: their rows, their key row
// (guarded on the n read, so a key with new input keeps everything), then only
// the objects that row named. The local raw cap deletes raw day folders,
// applied in/ files and calls/ objects past it; the recent index is trimmed a
// day behind the tail's cursor.

const day = 24 * time.Hour

// rowsOf is how many rows every stored and rendered table holds for key.
func (e *env) rowsOf(key string) int {
	e.t.Helper()
	sid, _ := strings.CutPrefix(key, "s:")
	n := 0
	for _, t := range append(storedTables(), render.Tables...) {
		owner, arg := "key", key
		if t.Kind != pg.Stored {
			if owner = render.OwnerColumn(t.Name); owner != "key" {
				arg = sid
			}
		}
		var c int
		if err := e.db.QueryRow(e.ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", t.Name, owner), arg).Scan(&c); err != nil {
			e.t.Fatal(err)
		}
		n += c
	}
	return n
}

// objects is the store's object keys under the key's folder.
func (e *env) objects(key string) []string {
	e.t.Helper()
	objs, err := e.store.List(e.ctx, rollup.KeyDir(prefix, key))
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, o := range objs {
		out = append(out, strings.TrimPrefix(o.Key, rollup.KeyDir(prefix, key)))
	}
	return out
}

func (e *env) exists(key string) bool {
	_, err := e.store.Get(e.ctx, key)
	return err == nil
}

func (e *env) hasRow(key string) bool {
	e.t.Helper()
	var ok bool
	if err := e.db.QueryRow(e.ctx, "SELECT EXISTS (SELECT FROM keys WHERE key = $1)", key).Scan(&ok); err != nil {
		e.t.Fatal(err)
	}
	return ok
}

// An old key is forgotten whole, and a recent one is untouched.
func TestRetentionForgetsKeysOlderThanTheWindow(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	old, recent := sessionKey("old"), sessionKey("recent")
	e.ingestAt("old", 8, e.clock.Now().Add(-20*day))
	e.ingestAt("old", 4, e.clock.Now().Add(-20*day+time.Hour))
	e.ingest("recent", 8)
	e.drain(w)
	if d := e.keyRow(old).Day; d != "2026-10-24" {
		t.Fatalf("old key's day %q, want 2026-10-24", d)
	}
	if e.rowsOf(old) == 0 || len(e.objects(old)) == 0 {
		t.Fatal("vacuous: the old key has no rows or objects")
	}
	// Its drawer rows and their templates (§3.7) go with it.
	if e.count("drawer_requests", old) == 0 || e.count("drawer_events", old) == 0 || e.count("drawer_templates", old) == 0 {
		t.Fatal("vacuous: the old key has no drawer rows")
	}
	before := e.rowsOf(recent)

	res, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Forgotten != 1 {
		t.Errorf("forgot %d keys, want 1", res.Forgotten)
	}
	if e.hasRow(old) {
		t.Error("the old key's row is still there")
	}
	if n := e.rowsOf(old); n != 0 || e.count("drawer_requests", old)+e.count("drawer_events", old)+e.count("drawer_templates", old) != 0 {
		t.Errorf("the old key still has %d rows", n)
	}
	if objs := e.objects(old); len(objs) != 0 {
		t.Errorf("the old key's objects are still there: %v", objs)
	}
	if n := e.rowsOf(recent); n != before || !e.hasRow(recent) {
		t.Errorf("the recent key has %d rows (was %d), row %v", n, before, e.hasRow(recent))
	}
	e.checkAt(0, recent)
}

// The window is whole days: a key is kept through its day + Days.
func TestRetentionKeepsTheWindow(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("edge")
	e.ingestAt("edge", 4, e.clock.Now().Add(-15*day)) // 2026-10-29: exactly 15 days back
	e.drain(w)
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if !e.hasRow(key) {
		t.Fatal("a key 15 days back was forgotten with a 15-day window")
	}
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now().Add(day)); err != nil {
		t.Fatal(err)
	}
	if e.hasRow(key) {
		t.Fatal("a key 16 days back was kept with a 15-day window")
	}
}

// An account key has no day: retention never forgets it. Days 0 is off.
func TestRetentionKeepsAccountKeysAndCanBeOff(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	if _, err := e.db.Exec(e.ctx, `INSERT INTO keys (key, n, day) VALUES ('a:acct', 3, ''), ('s:old', 1, '2020-01-01')`); err != nil {
		t.Fatal(err)
	}
	if res, err := w.Retain(e.ctx, Retention{}, e.clock.Now()); err != nil || res.Forgotten != 0 || !e.hasRow("s:old") {
		t.Fatalf("Days 0 forgot %d keys (err %v)", res.Forgotten, err)
	}
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if !e.hasRow("a:acct") {
		t.Error("an account key was forgotten")
	}
	if e.hasRow("s:old") {
		t.Error("an old key was kept")
	}
}

// A key that committed after the pass read it keeps its row and its rows:
// the delete is guarded on the n it read.
func TestRetentionKeepsAKeyThatGotNewInput(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("s")
	e.ingestAt("s", 4, e.clock.Now().Add(-20*day))
	e.drain(w)
	row := e.keyRow(key)
	e.ingestAt("s", 2, e.clock.Now().Add(-20*day+time.Minute))
	e.drain(w)
	_, ok, err := w.forget(e.ctx, key, row.N, "2026-10-29")
	if err != nil {
		t.Fatal(err)
	}
	if ok || !e.hasRow(key) || e.rowsOf(key) == 0 {
		t.Fatalf("forgot a key at a newer n (forgot %v, row %v)", ok, e.hasRow(key))
	}
	for _, c := range e.keyRow(key).State {
		if !e.exists(rollup.KeyDir(prefix, key) + c) {
			t.Errorf("state chunk %s was deleted", c)
		}
	}
	e.check()
}

// Only what the deleted row named goes: its state chunks, its garbage and its
// in/ files up to its last applied. A file that came in after stays, and the
// key starts again from it.
func TestRetentionDeletesOnlyWhatTheRowNamed(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("s")
	e.ingestAt("s", 4, e.clock.Now().Add(-20*day))
	e.drain(w)
	row := e.keyRow(key)
	stray := rollup.KeyDir(prefix, key) + rollup.StatePath(99, "stray") // not the row's: another writer's
	if err := e.store.Put(e.ctx, stray, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(e.ctx, `UPDATE keys SET garbage = '[{"path":"state/000000000000-gone","delete_after_ns":1}]'`); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Put(e.ctx, rollup.KeyDir(prefix, key)+"state/000000000000-gone", []byte("g")); err != nil {
		t.Fatal(err)
	}
	// New input lands but isn't applied yet: its file sorts after last_applied.
	e.all = nil
	late := e.ingest("s", 3)
	if late <= row.LastApplied {
		t.Fatalf("vacuous: %s sorts before %s", late, row.LastApplied)
	}
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if e.hasRow(key) {
		t.Fatal("the key wasn't forgotten")
	}
	got := e.objects(key)
	want := []string{"in/" + late, rollup.StatePath(99, "stray")}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("objects left %v, want %v", got, want)
	}
	// The flag stays, and the key starts over from the file that's left.
	e.drain(w)
	if n := e.keyRow(key).N; n != 1 {
		t.Errorf("the restarted key is at n %d, want 1", n)
	}
	e.check()
}

// A key whose row is locked (a commit in flight) is skipped after lock_timeout
// and forgotten on a later pass.
func TestRetentionSkipsALockedKey(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("s")
	e.ingestAt("s", 4, e.clock.Now().Add(-20*day))
	e.drain(w)
	tx, err := e.db.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, "SELECT FROM keys WHERE key = $1 FOR UPDATE", key); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now())
	if err != nil {
		t.Fatalf("a locked key failed the pass: %v", err)
	}
	if res.Forgotten != 0 || res.Skipped != 1 || !e.hasRow(key) {
		t.Fatalf("locked key: %+v, row %v", res, e.hasRow(key))
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("waited %v for the lock", d)
	}
	_ = tx.Rollback(e.ctx)
	if res, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now()); err != nil || res.Forgotten != 1 {
		t.Fatalf("next pass: %+v %v", res, err)
	}
}

// Rows go in statements of at most retainBatch rows each.
func TestRetentionDeletesInBatches(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("s")
	e.ingestAt("s", 12, e.clock.Now().Add(-20*day))
	e.drain(w)
	defer func(b int) { retainBatch = b }(retainBatch)
	retainBatch = 2
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if n := e.rowsOf(key); n != 0 || e.hasRow(key) {
		t.Fatalf("%d rows left, row %v", n, e.hasRow(key))
	}
}

// A worker that held the forgotten key warm loses its guard, drops the state
// and starts the key over from new input alone.
func TestRetentionThenAWarmWorker(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("s")
	e.ingestAt("s", 4, e.clock.Now().Add(-20*day))
	e.drain(w)
	if !w.Warm(key) {
		t.Fatal("vacuous: the key isn't warm")
	}
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.all = nil
	e.ingest("s", 3)
	e.drain(w)
	e.check()
}

// The local raw cap: raw day folders, applied in/ files and (on a local
// directory) calls/ objects older than RawDays go; the rest stay.
func TestRetentionRawCap(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	key := sessionKey("s")
	now := e.clock.Now()
	put := func(k string) {
		if err := e.store.Put(e.ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	rawOld := prefix + "/dt=2026-11-13/hour=03/instance=i/1-000001.ndjson.gz"
	capOld := prefix + "/capture/dt=2026-11-13/hour=03/instance=i/1-000001.ndjson.gz"
	rawNew := prefix + "/dt=2026-11-14/hour=00/instance=i/1-000002.ndjson.gz"
	callOld := prefix + "/calls/old.ndjson.gz"
	put(rawOld)
	put(capOld)
	put(rawNew)
	put(callOld)
	put(prefix + "/calls/old.keys/" + key)
	oldIn := e.ingestAt("s", 2, now.Add(-4*day))
	e.clock.Advance(4 * day)
	put(prefix + "/calls/new.ndjson.gz")
	midIn := e.ingest("s", 2)
	e.drain(w)
	// A file in the grace window before last_applied: applied, but not deletable.
	e.clock.Advance(time.Minute)
	lastIn := e.ingest("s", 2)
	e.drain(w)
	if e.keyRow(key).LastApplied != lastIn {
		t.Fatal("vacuous: the last file wasn't applied")
	}
	r := Retention{Days: 15, RawDays: 3}
	if _, err := w.Retain(e.ctx, r, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]bool{
		rawOld: false, capOld: false, rawNew: true,
		callOld:                              true, // calls/ only on a local directory store
		rollup.InPrefix(prefix, key) + oldIn: false,
		rollup.InPrefix(prefix, key) + midIn: true, rollup.InPrefix(prefix, key) + lastIn: true,
	} {
		if e.exists(k) != want {
			t.Errorf("%s exists %v, want %v", k, !want, want)
		}
	}
	r.Calls = true
	if _, err := w.Retain(e.ctx, r, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if e.exists(callOld) || e.exists(prefix+"/calls/old.keys/"+key) {
		t.Error("calls/ objects past the cap stayed on a local directory store")
	}
	if !e.exists(prefix + "/calls/new.ndjson.gz") {
		t.Error("a calls/ object inside the cap was deleted")
	}
	e.check()
}

// The recent index is trimmed a day behind the tail's cursor, raw cap or not.
func TestRetentionTrimsTheRecentIndex(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	dir := keyflag.Prefix(prefix)
	name := func(at time.Time, seq int) string {
		return fmt.Sprintf("%s-inc-%012d.json", at.UTC().Format(keyflag.TimeFormat), seq)
	}
	now := e.clock.Now()
	stale, kept, cursor := name(now.Add(-25*time.Hour), 1), name(now.Add(-23*time.Hour), 2), name(now, 3)
	for _, n := range []string{stale, kept, cursor} {
		if err := e.store.Put(e.ctx, dir+n, []byte(`{"keys":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(e.ctx, "UPDATE tail SET last_name = $1", cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Retain(e.ctx, Retention{Days: 15}, now); err != nil {
		t.Fatal(err)
	}
	if e.exists(dir + stale) {
		t.Error("an index object a day behind the cursor stayed")
	}
	if !e.exists(dir+kept) || !e.exists(dir+cursor) {
		t.Error("an index object inside the day was deleted")
	}
}

// The singleton runs hourly under its own lock, and the environment sets it.
func TestRetentionSingletonAndEnv(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	s := RetentionSingleton(w, Retention{Days: 15})
	if s.Lock != RetentionLock || s.Every != time.Hour || s.Name != "retention" {
		t.Errorf("singleton %+v", s)
	}
	if err := s.Run(e.ctx, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	for env, want := range map[[2]string]Retention{
		{"", ""}:     {Days: 15},
		{"7", "3"}:   {Days: 7, RawDays: 3},
		{"0", "0"}:   {},
		{"x", "y"}:   {Days: 15},
		{"30", "-1"}: {Days: 30},
	} {
		got := retentionFromEnv(func(k string) string {
			return map[string]string{"ROLLUP_RETENTION_DAYS": env[0], "ROLLUP_RETENTION_RAW_DAYS": env[1]}[k]
		})
		if got != want {
			t.Errorf("env %v: %+v, want %+v", env, got, want)
		}
	}
}
