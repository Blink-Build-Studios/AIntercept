package keyflag

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Ingest's recent index (docs/PIPELINE.md §1 step 4): each ingest file's key
// is journaled in the spool before the file's upload; once the upload
// completes the key goes into the next index object, uploaded every 5 s (and
// at shutdown) as v1/recent/<s3-time>-<incarnation>-<seq>.json. At start the
// pod uploads whatever a crash left in the journal. No Redis, no Postgres.

// puts records each index object handed to the spool: its template and body.
type puts struct {
	mu   sync.Mutex
	objs []put
	fail int
}

type put struct {
	tmpl string
	body Object
}

func (p *puts) put(tmpl string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail > 0 {
		p.fail--
		return errors.New("spool write failed")
	}
	var o Object
	if err := json.Unmarshal(body, &o); err != nil {
		return err
	}
	p.objs = append(p.objs, put{tmpl, o})
	return nil
}

func (p *puts) take() []put {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.objs
	p.objs = nil
	return out
}

var t0 = time.Date(2027, 3, 1, 10, 7, 35, 0, time.UTC)

// file is one ingest file of key: its template (journaled) and where it landed.
func file(prefix, key string, at time.Time, seq int64) (tmpl, landed string) {
	tmpl = InTemplate(prefix, key, "inc", seq)
	return tmpl, events.ResolveUploadKey(tmpl, at)
}

func open(t *testing.T, dir string, p *puts) *Index {
	t.Helper()
	x, err := Open(Config{Dir: dir, Prefix: "p", Incarnation: "inc-2", Put: p.put, SkipFsync: true})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// Atomic: an index object is named by its S3 time to the second, so one cursor tails them.
func TestIndexNamesAreTimeOrdered(t *testing.T) {
	tmpl := Template("p", "inc", 3)
	if want := "p/v1/recent/" + events.UploadTime + "-inc-000000000003.json"; tmpl != want {
		t.Fatalf("template %q, want %q", tmpl, want)
	}
	if TimeFormat != events.UploadTimeFormat {
		t.Fatalf("index time format %q, writer's %q", TimeFormat, events.UploadTimeFormat)
	}
	if Prefix("") != "v1/recent/" || Prefix("/p/") != "p/v1/recent/" {
		t.Fatalf("prefix %q %q", Prefix(""), Prefix("/p/"))
	}
	a := strings.TrimPrefix(events.ResolveUploadKey(Template("p", "zzz", 9), t0), Prefix("p"))
	b := strings.TrimPrefix(events.ResolveUploadKey(Template("p", "aaa", 1), t0.Add(time.Second)), Prefix("p"))
	if !(a < b) {
		t.Fatalf("%q sorts after %q", a, b)
	}
	if at, err := NameTime(a); err != nil || !at.Equal(t0) {
		t.Fatalf("NameTime(%q) = %v, %v", a, at, err)
	}
	if _, err := NameTime("junk.json"); err == nil {
		t.Fatal("a name without a time parsed")
	}
}

// Atomic: a landed file's key goes into the next index object, once each, with
// the pod's newest upload time; the key isn't indexed before its upload completes.
func TestIndexListsLandedKeys(t *testing.T) {
	p := &puts{}
	x := open(t, t.TempDir(), p)
	tb1, lb1 := file("p", "s:b", t0, 1)
	ta2, la2 := file("p", "s:a", t0, 2)
	ta3, la3 := file("p", "s:a", t0.Add(3*time.Second), 3)
	tc4, _ := file("p", "c:k:2027-03-01", t0, 4)
	for _, f := range []struct{ key, tmpl string }{{"s:b", tb1}, {"s:a", ta2}, {"s:a", ta3}, {"c:k:2027-03-01", tc4}} {
		if err := x.Journal(f.key, f.tmpl); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Flush(); err != nil || len(p.take()) != 0 {
		t.Fatal("an index object before any upload completed")
	}
	for _, k := range []string{lb1, la2, la3} {
		x.Landed("s3", k)
	}
	x.Landed("s3", "p/dt=2027-03-01/hour=10/x.ndjson.gz")                  // not an ingest file
	x.Landed("s3", events.ResolveUploadKey(Template("p", "inc-2", 1), t0)) // an index object itself
	if err := x.Flush(); err != nil {
		t.Fatal(err)
	}
	got := p.take()
	if len(got) != 1 {
		t.Fatalf("objects %+v, want 1", got)
	}
	if want := Template("p", "inc-2", 1); got[0].tmpl != want {
		t.Fatalf("template %q, want %q", got[0].tmpl, want)
	}
	o := got[0].body
	if o.Incarnation != "inc-2" || !equal(o.Keys, []string{"s:a", "s:b"}) || o.LastNS != t0.Add(3*time.Second).UnixNano() {
		t.Fatalf("object %+v", o)
	}
	if err := x.Flush(); err != nil || len(p.take()) != 0 {
		t.Fatal("a flush with nothing new wrote an object")
	}
	_, l4 := file("p", "c:k:2027-03-01", t0, 4)
	x.Landed("s3", l4)
	if err := x.Flush(); err != nil {
		t.Fatal(err)
	}
	got = p.take()
	if len(got) != 1 || got[0].tmpl != Template("p", "inc-2", 2) || !equal(got[0].body.Keys, []string{"c:k:2027-03-01"}) {
		t.Fatalf("second object %+v", got)
	}
}

// Atomic: a failed index write keeps its keys for the next flush.
func TestFailedIndexWriteKeepsTheKeys(t *testing.T) {
	p := &puts{fail: 1}
	x := open(t, t.TempDir(), p)
	tm, l := file("p", "s:a", t0, 1)
	_ = x.Journal("s:a", tm)
	x.Landed("s3", l)
	if err := x.Flush(); err == nil {
		t.Fatal("the failed write wasn't reported")
	}
	if err := x.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := p.take(); len(got) != 1 || !equal(got[0].body.Keys, []string{"s:a"}) {
		t.Fatalf("objects %+v", got)
	}
}

// A pod crash: the journal survives in the spool, and at start the pod
// uploads whatever it holds: keys whose files landed but weren't indexed yet,
// and keys whose files hadn't landed (harmless: their runs find nothing). A
// torn last line is skipped.
func TestJournalSurvivesACrash(t *testing.T) {
	dir := t.TempDir()
	p := &puts{}
	x := open(t, dir, p)
	ta, la := file("p", "s:landed", t0, 1)
	tb, _ := file("p", "s:in-flight", t0, 2)
	_ = x.Journal("s:landed", ta)
	_ = x.Journal("s:in-flight", tb)
	x.Landed("s3", la)
	// The crash: no flush, no close. A half-written line ends the journal.
	f, err := os.OpenFile(filepath.Join(dir, journalName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"k":"s:tor`)
	f.Close()

	y := open(t, dir, p) // the next start
	if err := y.Flush(); err != nil {
		t.Fatal(err)
	}
	got := p.take()
	if len(got) != 1 || !equal(got[0].body.Keys, []string{"s:in-flight", "s:landed"}) || got[0].body.LastNS != 0 {
		t.Fatalf("objects at start %+v", got)
	}
	// Uploaded: a second restart has nothing left over.
	z := open(t, dir, p)
	if err := z.Flush(); err != nil || len(p.take()) != 0 {
		t.Fatalf("leftovers uploaded twice (%v)", err)
	}
}

// The journal keeps exactly the keys whose files are still in flight after
// each flush: a crash then re-uploads only those.
func TestJournalKeepsOnlyFilesInFlight(t *testing.T) {
	dir := t.TempDir()
	p := &puts{}
	x := open(t, dir, p)
	ta, la := file("p", "s:a", t0, 1)
	tb, _ := file("p", "s:b", t0, 2)
	ta3, _ := file("p", "s:a", t0, 3)
	_ = x.Journal("s:a", ta)
	_ = x.Journal("s:b", tb)
	_ = x.Journal("s:a", ta3) // a second file of s:a, still in flight after the first lands
	x.Landed("s3", la)
	if err := x.Flush(); err != nil {
		t.Fatal(err)
	}
	p.take()
	y := open(t, dir, p)
	if err := y.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := p.take(); len(got) != 1 || !equal(got[0].body.Keys, []string{"s:a", "s:b"}) {
		t.Fatalf("leftovers %+v, want s:a's second file and s:b", got)
	}
}

// Files a previous process left in the spool land in this one: never
// journaled here, they're still indexed.
func TestUnjournaledLandingsAreIndexed(t *testing.T) {
	p := &puts{}
	x := open(t, t.TempDir(), p)
	_, l := file("p", "s:old", t0, 9)
	x.Landed("s3", l)
	if err := x.Close(); err != nil { // the shutdown flush
		t.Fatal(err)
	}
	if got := p.take(); len(got) != 1 || !equal(got[0].body.Keys, []string{"s:old"}) {
		t.Fatalf("objects %+v", got)
	}
}

// The cadence: Run uploads on every tick (5 s by default), and Close uploads
// the rest at shutdown.
func TestRunUploadsEveryTickAndAtClose(t *testing.T) {
	if DefaultEvery != 5*time.Second {
		t.Fatalf("default cadence %v, want 5s", DefaultEvery)
	}
	p := &puts{}
	tick := make(chan time.Time)
	x, err := Open(Config{Dir: t.TempDir(), Prefix: "p", Incarnation: "inc-2", Put: p.put, SkipFsync: true,
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
			if d != DefaultEvery {
				t.Errorf("ticker %v, want %v", d, DefaultEvery)
			}
			return tick, func() {}
		}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); x.Run() }()
	for i := int64(1); i <= 3; i++ {
		tm, l := file("p", "s:a", t0, i)
		_ = x.Journal("s:a", tm)
		x.Landed("s3", l)
		tick <- t0
		waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.objs) == int(i) })
	}
	tm, l := file("p", "s:z", t0, 4)
	_ = x.Journal("s:z", tm)
	x.Landed("s3", l)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	got := p.take()
	if len(got) != 4 || !equal(got[3].body.Keys, []string{"s:z"}) {
		t.Fatalf("objects %+v", got)
	}
	if err := x.Journal("s:late", tm); err == nil {
		t.Fatal("journaled after Close")
	}
}

// End to end through the observer's writer: a file lands, the pod crashes
// before its index goes out (the spool survives), and the restarted pod's
// index names the key in the store, under a time name.
func TestCrashBetweenUploadAndIndexThroughTheWriter(t *testing.T) {
	spool, out := t.TempDir(), t.TempDir()
	target, err := events.NewDirTarget("local", out)
	if err != nil {
		t.Fatal(err)
	}
	start := func() (*events.Writer, *Index) {
		var stream *events.ObjectStream
		x, err := Open(Config{Dir: filepath.Join(spool, "recent"), Prefix: "", Incarnation: "inc", SkipFsync: true,
			Put: func(tmpl string, body []byte) error { return stream.PutAtUploadDurable(tmpl, body, "") }})
		if err != nil {
			t.Fatal(err)
		}
		w, err := events.NewWriter(events.WriterConfig{SpoolDir: spool, Targets: []events.Target{target}, SkipFsync: true,
			OnUploaded: x.Landed})
		if err != nil {
			t.Fatal(err)
		}
		stream = w.NewObjectStream(events.ObjectStreamConfig{Target: "local", ContentType: "application/json"})
		return w, x
	}
	w, x := start()
	files := w.NewObjectStream(events.ObjectStreamConfig{Target: "local"})
	tmpl := InTemplate("", "s:crash", "inc", 1)
	if err := x.Journal("s:crash", tmpl); err != nil {
		t.Fatal(err)
	}
	if err := files.PutAtUploadDurable(tmpl, []byte("parquet"), ""); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil { // the file lands...
		t.Fatal(err)
	}
	w.Abandon() // ...and the pod dies before its index goes out

	w, x = start()
	if err := x.Flush(); err != nil { // at start: the journal's leftovers
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	names, _ := filepath.Glob(filepath.Join(out, "v1", "recent", "*.json"))
	if len(names) != 1 {
		t.Fatalf("index objects %v, want 1", names)
	}
	if _, err := NameTime(filepath.Base(names[0])); err != nil {
		t.Fatalf("index object %s isn't time-named: %v", names[0], err)
	}
	b, _ := os.ReadFile(names[0])
	var o Object
	if err := json.Unmarshal(b, &o); err != nil || !equal(o.Keys, []string{"s:crash"}) {
		t.Fatalf("index %s (%v)", b, err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

func equal(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
