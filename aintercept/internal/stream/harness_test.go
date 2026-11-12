package stream

import (
	"context"
	"fmt"
	"math/bits"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// gen makes synthetic records: calls and events across a set of sessions and
// credentials, with times that wander backwards as well as forwards, as real
// late data does.
type gen struct {
	rng      *rand.Rand
	sessions int
	n        int64
	ts       int64
}

func newGen(seed int64, sessions int) *gen {
	return &gen{rng: rand.New(rand.NewSource(seed)), sessions: sessions, ts: 1_800_000_000_000_000_000}
}

func (g *gen) one() records.Record {
	g.n++
	g.ts += int64(g.rng.Intn(2_000_000_000)) - 500_000_000 // ±, mostly forwards
	id := fmt.Sprintf("pod-%d-%06d", g.rng.Intn(3), g.n)
	var r records.Record
	switch g.rng.Intn(4) {
	case 0: // a call with no session: routed by credential and day
		r = records.Record{Kind: records.KindCallEnd, Call: &canon.Request{CallID: id, KeyHash: fmt.Sprintf("k%d", g.rng.Intn(3)), EndNS: g.ts}}
	case 1:
		r = records.Record{Kind: records.KindClientEvent, Event: &canon.Event{SessionID: fmt.Sprintf("s%d", g.rng.Intn(g.sessions)), RowNS: g.ts, NS: g.ts}}
	default:
		r = records.Record{Kind: records.KindCallEnd, Call: &canon.Request{CallID: id, SessionID: fmt.Sprintf("s%d", g.rng.Intn(g.sessions)), EndNS: g.ts}}
	}
	r.Version, r.RecordID = records.Version, id
	r.RoutingKey = records.RoutingKey(&r)
	return r
}

func (g *gen) batch(n int) []records.Record {
	out := make([]records.Record, n)
	for i := range out {
		out[i] = g.one()
	}
	return out
}

// harness uploads records the way ingest does (one file per bucket per upload,
// under the upload minute) and checks tables against one pass.
type harness struct {
	t        *testing.T
	s        Store
	clock    *FakeClock
	l        Layout
	bits     int
	gen      *gen
	m        Model
	uploaded []records.Record
	seq      int64
}

func newHarness(t *testing.T, s Store, clock *FakeClock, l Layout, buckets int) *harness {
	if buckets&(buckets-1) != 0 {
		t.Fatalf("buckets must be a power of two: %d", buckets)
	}
	return &harness{t: t, s: s, clock: clock, l: l, bits: bits.TrailingZeros(uint(buckets)), gen: newGen(1, 12), m: CountModel{}}
}

func (h *harness) buckets() int { return 1 << h.bits }

func (h *harness) upload(pod int, rs []records.Record) {
	h.t.Helper()
	h.uploadAt(pod, rs, h.clock.Now())
}

func (h *harness) uploadAt(pod int, rs []records.Record, at time.Time) {
	h.t.Helper()
	by := map[int][]records.Record{}
	for _, r := range rs {
		b := records.BucketOf(r.RoutingKey, h.bits)
		r.Bucket = int32(b)
		by[b] = append(by[b], r)
	}
	for b, part := range by {
		h.seq++
		body, err := records.Encode(part)
		if err != nil {
			h.t.Fatal(err)
		}
		key := records.ObjectKey(h.l.Prefix, b, at, fmt.Sprintf("pod%d", pod), h.seq)
		if err := h.s.Put(context.Background(), key, body); err != nil {
			h.t.Fatal(err)
		}
	}
	h.uploaded = append(h.uploaded, rs...)
}

func (h *harness) worker(replica string, epoch int64) *Bucket {
	var c Clock = RealClock{}
	if h.clock != nil {
		c = h.clock
	}
	return &Bucket{S: h.s, L: h.l, M: h.m, B: 0, Bits: h.bits, Epoch: epoch, Replica: replica, Clock: c}
}

// checkAll: every table, read from every bucket's latest commit, equals one
// pass over everything uploaded.
func (h *harness) checkAll(ctx context.Context) {
	h.t.Helper()
	got, err := ReadAll(ctx, h.s, h.l, h.m, h.buckets())
	if err != nil {
		h.t.Fatal(err)
	}
	want := Reference(h.m, h.uploaded)
	if !reflect.DeepEqual(canonical(got), canonical(want)) {
		h.t.Fatalf("tables differ from one pass:\n got %s\nwant %s", summary(got), summary(want))
	}
}

func (h *harness) checkEqualsReference(ctx context.Context, w *Bucket) {
	h.t.Helper()
	if _, err := w.Cycle(ctx); err != nil && err != ErrFenced {
		h.t.Fatal(err)
	}
	h.checkAll(ctx)
}

// unreferenced counts a bucket's table and state files no live commit names
// (nor its pending garbage).
func (h *harness) unreferenced(ctx context.Context, b int) int {
	h.t.Helper()
	c, err := LoadLatest(ctx, h.s, h.l, b)
	if err != nil {
		h.t.Fatal(err)
	}
	named := map[string]bool{}
	if c != nil {
		for _, k := range c.Files() {
			named[k] = true
		}
	}
	all, err := h.s.List(ctx, h.l.root())
	if err != nil {
		h.t.Fatal(err)
	}
	n := 0
	for _, o := range all {
		if !strings.Contains(o.Key, fmt.Sprintf("bucket=%02d/", b)) || strings.Contains(o.Key, "/leases/") {
			continue
		}
		if (strings.Contains(o.Key, "/tables/") || strings.Contains(o.Key, "/state/")) && !named[o.Key] {
			n++
		}
	}
	return n
}

func canonical(t map[string][]Row) map[string][]Row {
	out := map[string][]Row{}
	for name, rows := range t {
		rs := append([]Row(nil), rows...)
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].Key != rs[j].Key {
				return rs[i].Key < rs[j].Key
			}
			return rs[i].ID < rs[j].ID
		})
		if len(rs) > 0 {
			out[name] = rs
		}
	}
	return out
}

func summary(t map[string][]Row) string {
	var b strings.Builder
	for name, rows := range canonical(t) {
		fmt.Fprintf(&b, "%s:%d rows ", name, len(rows))
	}
	return b.String()
}
