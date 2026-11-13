package keyflag

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// After each ingest file lands (docs/PIPELINE.md §1): SADD dirty <key>,
// fire-and-forget; the key goes into this pod's recent index for the file's S3
// minute, uploaded once a minute; and the pod's newest ingest time goes to Redis.

type puts struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (p *puts) put(key string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.objs == nil {
		p.objs = map[string][]byte{}
	}
	p.objs[key] = body
	return nil
}

var t0 = time.Date(2027, 3, 1, 10, 7, 35, 0, time.UTC)

func inObj(prefix, key string, at time.Time, seq int64) string {
	return rollup.InPrefix(prefix, key) + rollup.InName(at, "inc", seq)
}

// Atomic: the template the writer fills in names a file exactly as the layout does.
func TestInTemplateResolvesToTheLayoutsName(t *testing.T) {
	got := events.ResolveUploadKey(InTemplate("p", "s:a/b", "inc", 7), t0)
	if want := rollup.InPrefix("p", "s:a/b") + rollup.InName(t0, "inc", 7); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if events.UploadTimeFormat != rollup.TimeFormat {
		t.Fatalf("writer time format %q, layout %q", events.UploadTimeFormat, rollup.TimeFormat)
	}
}

// Atomic: a landed ingest file flags its key dirty and records the ingest time.
func TestLandedFlagsTheKeyDirty(t *testing.T) {
	mr := miniredis.RunT(t)
	p := &puts{}
	f := New(Config{Redis: mr.Addr(), Prefix: "p", Incarnation: "inc", Put: p.put})
	defer f.Close()
	f.Landed("s3", inObj("p", "s:abc", t0, 1))
	f.Landed("s3", "p/dt=2027-03-01/hour=10/x.ndjson.gz") // not an ingest file: ignored
	f.Wait()
	members, err := mr.Members("dirty")
	if err != nil || len(members) != 1 || members[0] != "s:abc" {
		t.Fatalf("dirty %v (%v), want [s:abc]", members, err)
	}
	if v, err := mr.Get("ingest:last:inc"); err != nil || v != fmt.Sprint(t0.UnixNano()) {
		t.Fatalf("ingest:last %q (%v)", v, err)
	}
}

// Atomic: Redis down: Landed never blocks, and the recent index is still written.
func TestRedisDownNeverBlocksAndTheIndexStillGoesOut(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close() // nothing listens here
	p := &puts{}
	f := New(Config{Redis: addr, Prefix: "p", Incarnation: "inc", Put: p.put})
	defer f.Close()
	began := time.Now()
	for i := 0; i < 5000; i++ {
		f.Landed("s3", inObj("p", "s:abc", t0, int64(i)))
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("5000 Landed calls took %v with Redis down", d)
	}
	if err := f.FlushRecent(); err != nil {
		t.Fatal(err)
	}
	if len(p.objs) != 1 {
		t.Fatalf("recent index objects %v, want 1", keysOf(p.objs))
	}
}

// Atomic: one recent object per S3 minute the files landed in, listing each key once.
func TestRecentIndexContents(t *testing.T) {
	p := &puts{}
	f := New(Config{Prefix: "p", Incarnation: "inc", Put: p.put}) // no Redis
	defer f.Close()
	m2 := t0.Add(time.Minute)
	f.Landed("s3", inObj("p", "s:b", t0, 1))
	f.Landed("s3", inObj("p", "s:a", t0, 2))
	f.Landed("s3", inObj("p", "s:a", t0, 3))
	f.Landed("s3", inObj("p", "c:k:2027-03-01", m2, 4))
	if err := f.FlushRecent(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		rollup.RecentName("p", t0, "inc", 1): {"s:a", "s:b"},
		rollup.RecentName("p", m2, "inc", 2): {"c:k:2027-03-01"},
	}
	if len(p.objs) != len(want) {
		t.Fatalf("objects %v, want %v", keysOf(p.objs), want)
	}
	for k, w := range want {
		var got []string
		if err := json.Unmarshal(p.objs[k], &got); err != nil {
			t.Fatalf("%s: %v (%v)", k, err, keysOf(p.objs))
		}
		if !equal(got, w) {
			t.Fatalf("%s lists %v, want %v", k, got, w)
		}
	}
	p.objs = nil
	if err := f.FlushRecent(); err != nil || len(p.objs) != 0 {
		t.Fatalf("a second flush with nothing new wrote %v (%v)", keysOf(p.objs), err)
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
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
