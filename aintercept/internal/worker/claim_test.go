package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// The flag rules (B1, B2), quarantine (B4), the heartbeat claim (S1), the lock
// connection (S5) and /lag (docs/PIPELINE.md §2).

// --- flags ----------------------------------------------------------------

// B1: a flag with nothing behind it (the tail names a key whose file never
// landed, or one already applied) is consumed by its run, which commits
// nothing; dirty_keys ends empty and /lag at 0.
func TestIdleFlagsAreConsumed(t *testing.T) {
	e := newEnv(t)
	e.ingest("a", 2)
	w := e.worker("w1")
	e.drain(w)
	e.flag(sessionKey("a"), sessionKey("never-landed"))
	e.clock.Advance(time.Minute)
	if lag, _ := w.Lag(e.ctx); lag < time.Minute {
		t.Fatalf("lag %v with two flags a minute old", lag)
	}
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.flags(); n != 0 {
		t.Fatalf("%d flags left", n)
	}
	if e.keyRow(sessionKey("a")).N != 1 || e.keyRow(sessionKey("never-landed")).N != 0 {
		t.Fatal("an idle run committed")
	}
	if lag, err := w.Lag(e.ctx); err != nil || lag != 0 {
		t.Fatalf("lag %v %v", lag, err)
	}
	e.check()
}

// B2: a run cut short by the 500-file cap commits what it read and keeps its
// flag, with since set to the S3 time of the first file left.
func TestCappedRunKeepsItsFlag(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	var names []string
	for i := 0; i < 5; i++ {
		names = append(names, e.ingest("a", 1))
		e.clock.Advance(time.Second)
	}
	w := e.worker("w1", func(c *Config) { c.MaxFiles = 2 })
	if res := e.run(w, key); res.Applied != 2 || !res.Committed {
		t.Fatalf("capped run %+v", res)
	}
	f := e.flagOf(key)
	left, _ := rollup.InTime(names[2])
	if !f.ok || !f.since.Equal(left) {
		t.Fatalf("flag %+v, want it kept since %v (the first file left)", f, left)
	}
	if lag, _ := w.Lag(e.ctx); lag < e.clock.Now().Sub(left) {
		t.Fatalf("lag %v, want at least the first file left's wait", lag)
	}
	e.drain(w)
	if e.flags() != 0 || e.keyRow(key).N != 3 {
		t.Fatalf("after the drain: %d flags, n %d (want 0, 3)", e.flags(), e.keyRow(key).N)
	}
	e.check()
}

// --- the claim --------------------------------------------------------------

// Two live workers share the flagged keys: each takes at most its share per
// tick, never a key the other holds, and keeps its keys while they're flagged.
func TestHeartbeatClaimSharesKeys(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 20; i++ {
		e.ingest(fmt.Sprintf("s%02d", i), 1)
	}
	w1, w2 := e.worker("w1"), e.worker("w2")
	if err := w2.Heartbeat(e.ctx); err != nil { // w2 is live before w1's first claim
		t.Fatal(err)
	}
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(w1.Held()); n != 10 {
		t.Fatalf("w1 took %d keys, want its share of 20 over 2 live workers: 10", n)
	}
	// w2's share of the 10 left is 5 a tick; the rest go over the next ticks.
	if err := w2.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(w2.Held()); n != 5 {
		t.Fatalf("w2 took %d keys, want 5", n)
	}
	for i := 0; i < 5 && e.flags() > 0; i++ {
		_ = w2.Tick(e.ctx)
		_ = w1.Tick(e.ctx)
	}
	both := map[string]bool{}
	for _, k := range append(w1.Held(), w2.Held()...) {
		if both[k] {
			t.Fatalf("%s held by both", k)
		}
		both[k] = true
	}
	if len(both) != 20 || e.flags() != 0 {
		t.Fatalf("%d keys held, %d flags left", len(both), e.flags())
	}
	// A key w1 holds that's flagged again is run by w1 on its next tick, not taken by w2.
	k := w1.Held()[0]
	e.clock.Advance(time.Second)
	e.ingest(strings.TrimPrefix(k, "s:"), 1)
	if err := w2.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.flagOf(k).ok {
		t.Fatal("w2 ran a key w1 holds")
	}
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.flagOf(k).ok {
		t.Fatal("w1 didn't run its own flagged key")
	}
	e.check()
}

// The worker never reads pg_locks or pg_stat_activity: live workers come from
// heartbeats.
func TestNoLockTableReads(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"pg_locks", "pg_stat_activity"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s reads %s", f, bad)
			}
		}
	}
}

// A held key stays warm until it's had no input for Idle, then it's unlocked;
// Release unlocks everything.
func TestIdleKeysAreReleased(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 1)
	w := e.worker("w1")
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.locked(key) {
		t.Fatal("the claimed key isn't locked")
	}
	e.clock.Advance(30 * time.Second)
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.Held()) != 1 {
		t.Fatal("released before Idle")
	}
	e.clock.Advance(31 * time.Second)
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.Held()) != 0 || e.locked(key) {
		t.Fatal("an idle key is still held")
	}
	e.ingest("a", 1)
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	w.Release(e.ctx)
	if len(w.Held()) != 0 || e.locked(key) {
		t.Fatal("Release left a lock")
	}
	e.check()
}

// locked is whether some session holds key's advisory lock.
func (e *env) locked(key string) bool {
	e.t.Helper()
	conn, err := e.db.Acquire(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(e.ctx, "SELECT pg_try_advisory_lock("+lockOf("$1")+")", key).Scan(&got); err != nil {
		e.t.Fatal(err)
	}
	if got {
		_, _ = conn.Exec(e.ctx, "SELECT pg_advisory_unlock("+lockOf("$1")+")", key)
	}
	return !got
}

// S5: when the lock connection drops, the worker forgets every key it holds
// and their warm states: Postgres freed the locks, so another worker may hold
// them now.
func TestDroppedLockConnectionForgetsKeys(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 2)
	w1, w2 := e.worker("w1"), e.worker("w2")
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(w1.Held()) != 1 || !w1.Warm(key) {
		t.Fatal("w1 doesn't hold the key warm")
	}
	e.terminate(w1.LockPID())
	// w2 can take the key at once, and does.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	if err := w2.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(w2.Held()) != 1 {
		t.Fatal("w2 couldn't take the key whose lock w1 lost")
	}
	// w1's next tick notices: it forgets the key and its state, and reconnects.
	_ = w1.Tick(e.ctx)
	if len(w1.Held()) != 0 || w1.Warm(key) {
		t.Fatalf("w1 still holds %v (warm %v)", w1.Held(), w1.Warm(key))
	}
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatalf("w1 didn't reconnect: %v", err)
	}
	e.check()
}

// The lock connection lost mid-run: the run goes on (the lock is for affinity
// only), the guard decides, and the tables stay exact.
func TestLockLostMidRun(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	w2 := e.worker("w2")
	var w1 *Worker
	once := false
	w1 = e.worker("w1", withHook(func(step, _ string) error {
		if step == "read" && !once {
			once = true
			e.terminate(w1.LockPID()) // a partition: keepalives close w1's lock connection
			if err := w2.Tick(e.ctx); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}))
	if err := w1.Tick(e.ctx); err != nil && !errors.Is(err, ErrLockLost) {
		t.Fatal(err)
	}
	if e.keyRow(key).N != 1 {
		t.Fatalf("n %d, want one commit (the other lost its guard)", e.keyRow(key).N)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	e.drain(w1, w2)
	e.check()
}

// A COMMIT whose result is unknown (the connection dropped as it returned):
// the worker drops its warm state and flags the key again; the next run reads
// whichever n landed.
func TestCommitResultUnknown(t *testing.T) {
	for _, landed := range []bool{true, false} {
		t.Run(fmt.Sprint("landed=", landed), func(t *testing.T) {
			e := newEnv(t)
			key := sessionKey("a")
			e.ingest("a", 3)
			step := "commit-result" // after COMMIT returned
			if !landed {
				step = "rows" // before COMMIT: the server rolls it back when the connection goes
			}
			once := false
			w := e.worker("w1", withHook(func(s, _ string) error {
				if s == step && !once {
					once = true
					return fmt.Errorf("commit: %w", errUnknownResult)
				}
				return nil
			}))
			if _, err := w.RunKey(e.ctx, key); err == nil {
				t.Fatal("an unknown result wasn't an error")
			}
			if w.Warm(key) {
				t.Fatal("kept a warm state past an unknown commit")
			}
			if !e.flagOf(key).ok {
				t.Fatal("the key wasn't flagged again")
			}
			want := int64(0)
			if landed {
				want = 1
			}
			if n := e.keyRow(key).N; n != want {
				t.Fatalf("n %d, want %d", n, want)
			}
			e.drain(w)
			e.check()
		})
	}
}

// --- quarantine -------------------------------------------------------------

// B4: a key that fails 5 times in a row is quarantined: claims skip it, /lag
// ignores it, the gauge counts it. It's retried once an hour, and a commit
// releases it. Other keys carry on.
func TestQuarantine(t *testing.T) {
	e := newEnv(t)
	bad, good := sessionKey("bad"), sessionKey("good")
	failing := true
	w := e.worker("w1", withHook(func(step, key string) error {
		if key == bad && step == "read" && failing {
			return errors.New("poison")
		}
		return nil
	}))
	e.ingest("bad", 1)
	for i := 0; i < 5; i++ {
		_ = w.Tick(e.ctx)
		e.clock.Advance(time.Second)
	}
	f := e.flagOf(bad)
	if f.failures != 5 {
		t.Fatalf("failures %d after 5 failed runs", f.failures)
	}
	var lastErr string
	if err := e.db.QueryRow(e.ctx, "SELECT last_error FROM dirty_keys WHERE key = $1", bad).Scan(&lastErr); err != nil || !strings.Contains(lastErr, "poison") {
		t.Fatalf("last_error %q %v", lastErr, err)
	}
	if q, err := w.Quarantined(e.ctx); err != nil || q != 1 {
		t.Fatalf("quarantined %d %v", q, err)
	}
	if lag, _ := w.Lag(e.ctx); lag != 0 {
		t.Fatalf("lag %v: a quarantined key pins it", lag)
	}
	// Claims skip it; the good key is run.
	w.Release(e.ctx)
	e.ingest("good", 2)
	_ = w.Tick(e.ctx)
	if e.flagOf(good).ok || contains(w.Held(), bad) {
		t.Fatalf("held %v; good flagged %v", w.Held(), e.flagOf(good).ok)
	}
	if f := e.flagOf(bad); f.failures != 5 {
		t.Fatalf("a quarantined key was retried within the hour (failures %d)", f.failures)
	}
	// An hour on, it's retried; still failing, it waits another hour.
	e.clock.Advance(time.Hour)
	_ = w.Tick(e.ctx)
	if f := e.flagOf(bad); f.failures != 6 {
		t.Fatalf("failures %d, want the hourly retry", f.failures)
	}
	_ = w.Tick(e.ctx)
	if f := e.flagOf(bad); f.failures != 6 {
		t.Fatal("retried again within the hour")
	}
	// Fixed: the next hourly retry commits, which releases it.
	failing = false
	e.clock.Advance(time.Hour + time.Second)
	_ = w.Tick(e.ctx)
	if e.flagOf(bad).ok {
		t.Fatal("a successful commit didn't release the key")
	}
	if q, _ := w.Quarantined(e.ctx); q != 0 {
		t.Fatalf("quarantined %d after the release", q)
	}
	e.check()
}

// Hostile input: NUL in every string field and 5 KB ids, cleaned where records
// are built (records.Clean), commit like any other; the stored values are the
// sanitized ones. A record that skipped the cleaning can't wedge anything: its
// key fails, is quarantined after 5 tries, and the rest drain.
func TestHostileInput(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("x", 5000)
	nul := "a\x00b"
	sid := long + "\x00"
	c := ccCall(long+"c", sid, e.clock.Now().UnixNano())
	c.CorrelationID, c.RequestID, c.PromptID, c.Model, c.Path, c.ClientJSON = long+"\x00corr", long+"r", nul, nul, "/v1/\x00", `{"a":"\u0000"}`
	ev := e.event(sid, "tengu_api_success\x00", e.clock.Now().UnixNano(), `{"requestId":"`+long+`"}`)
	hostile := []records.Record{e.end(c, e.clock.Now().UnixNano()+1), ev,
		e.finish(records.Record{Kind: records.KindAccount, Account: &records.Account{AccountID: "acct\x00", Email: "e\x00@x", Name: long, NS: 1}})}
	for _, r := range hostile {
		if strings.ContainsRune(r.RoutingKey, 0) || len(r.RoutingKey) > 600 {
			t.Fatalf("an uncleaned routing key %q…", r.RoutingKey[:20])
		}
	}
	e.putFile(hostile, 0, e.clock.Now())
	w := e.worker("w1")
	e.drain(w)
	e.check()
	var n int
	if err := e.db.QueryRow(e.ctx, "SELECT count(*) FROM requests WHERE call_id LIKE 'sha256:%' AND session_id LIKE 'sha256:%'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d sanitized call rows, %v", n, err)
	}

	// Uncleaned: Postgres refuses the NUL; the key is quarantined, the others drain.
	raw := records.Record{Kind: records.KindCallEnd, Version: records.Version, RecordID: e.id(), CallID: "raw",
		Call: &canon.Request{CallID: "raw", SessionID: "raw", Path: "/\x00", EndNS: 5, Status: 200, Host: "api.anthropic.com"}}
	raw.RoutingKey = records.RoutingKey(&raw)
	b, err := records.Encode([]records.Record{raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Put(e.ctx, rollup.InPrefix(prefix, raw.RoutingKey)+rollup.InName(e.clock.Now(), "inc", 999), b); err != nil {
		t.Fatal(err)
	}
	e.flag(raw.RoutingKey)
	e.ingest("fine", 2)
	for i := 0; i < 8; i++ {
		_ = w.Tick(e.ctx)
		e.clock.Advance(time.Second)
	}
	if f := e.flagOf(raw.RoutingKey); f.failures != 5 {
		t.Fatalf("the poisoned key has %d failures, want 5 (quarantined)", f.failures)
	}
	if e.flagOf(sessionKey("fine")).ok {
		t.Fatal("another key was held up")
	}
	if lag, _ := w.Lag(e.ctx); lag != 0 {
		t.Fatalf("lag %v", lag)
	}
}

// --- lag ----------------------------------------------------------------------

// /lag is the larger of the oldest claimable flag's age and the tail's.
func TestLag(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	if lag, err := w.Lag(e.ctx); err != nil || lag != 0 {
		t.Fatalf("empty: %v %v", lag, err)
	}
	e.ingest("a", 1)
	e.clock.Advance(30 * time.Second)
	if lag, _ := w.Lag(e.ctx); lag != 30*time.Second {
		t.Fatalf("lag %v, want 30s", lag)
	}
	e.drain(w)
	if lag, _ := w.Lag(e.ctx); lag != 0 {
		t.Fatalf("lag %v after the drain", lag)
	}
	// A stuck tail shows as lag too (tail.Age, on Postgres's clock).
	if _, err := e.db.Exec(e.ctx, "UPDATE tail SET checked_at = now() - interval '45 seconds'"); err != nil {
		t.Fatal(err)
	}
	if lag, _ := w.Lag(e.ctx); lag < 45*time.Second || lag > 50*time.Second {
		t.Fatalf("lag %v, want the tail's 45s", lag)
	}
}

func TestHTTP(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	e.ingest("a", 1)
	e.clock.Advance(7 * time.Second)
	srv := httptest.NewServer(w.Handler())
	defer srv.Close()
	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/healthz"); code != 200 {
		t.Fatalf("/healthz %d", code)
	}
	code, body := get("/lag")
	var v struct {
		Oldest      float64 `json:"oldest_unapplied_seconds"`
		Quarantined int     `json:"quarantined_keys"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil || v.Oldest != 7 {
		t.Fatalf("/lag %d %s", code, body)
	}
	if code, body := get("/metrics"); code != 200 || !strings.Contains(body, "aintercept_quarantined_keys 0") {
		t.Fatalf("/metrics %d %s", code, body)
	}
	// Postgres unreachable: /lag errors, so KEDA holds the replica count.
	e.db.Close()
	w.Kill()
	if code, _ := get("/lag"); code != http.StatusServiceUnavailable {
		t.Fatalf("/lag with no database: %d", code)
	}
}

// A heartbeat row per worker.
func TestHeartbeatRow(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	var seen time.Time
	if err := e.db.QueryRow(e.ctx, "SELECT seen FROM workers WHERE worker = 'w1'").Scan(&seen); err != nil || !seen.Equal(e.clock.Now()) {
		t.Fatalf("seen %v %v", seen, err)
	}
}
