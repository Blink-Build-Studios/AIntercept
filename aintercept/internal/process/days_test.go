package process

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// longCapture is `days` days of one Claude Code session a day: a few calls and
// the analytics reporting on them, each call's rows in its own batch.
func longCapture(days int) *capture {
	c := &capture{files: map[string]*rawFile{}}
	for d := 0; d < days; d++ {
		sid := fmt.Sprintf("sess-day-%02d", d)
		for k := 0; k < 3; k++ {
			at := t0.Add(time.Duration(d)*24*time.Hour + time.Duration(k)*10*time.Minute)
			cid := fmt.Sprintf("obs-a-%d-%d", d, k)
			client := map[string]string{"x-claude-code-session-id": sid, "x-claude-code-prompt-id": sid + "-p"}
			c.add("obs-a", events.Event{CorrelationID: cid, Seq: 1, TimestampNS: ns(at), Phase: events.PhaseRequestHeaders,
				Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages"})
			c.add("obs-a", events.Event{CorrelationID: cid, Seq: 2, TimestampNS: ns(at.Add(5 * time.Second)), Phase: events.PhaseComplete,
				Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Status: 200, RequestStartNS: ns(at),
				AccountUUID: "acct-a", DeviceID: "dev-a", Client: client, Provider: map[string]string{"request-id": "req_" + cid},
				OutputTokens: 10})
			c.add("obs-b", events.Event{CorrelationID: "obs-b-an-" + cid, Seq: 1, TimestampNS: ns(at.Add(time.Minute)),
				Phase: events.PhaseClientEvent, Host: "api.anthropic.com", ClientEvent: &events.ClientEvent{Source: "claude_code",
					Name: "tengu_api_success", ID: "ev-" + cid, SessionID: sid, AccountUUID: "acct-a", DeviceID: "dev-a",
					Data: map[string]any{"additional_metadata": map[string]any{"requestId": "req_" + cid}}}})
		}
	}
	return c
}

// readLog records which keys a store was asked for.
type readLog struct {
	*memStore
	mu   sync.Mutex
	gets []string
}

func (r *readLog) Get(ctx context.Context, key string) ([]byte, error) {
	r.mu.Lock()
	r.gets = append(r.gets, key)
	r.mu.Unlock()
	return r.memStore.Get(ctx, key)
}

// Two months of history, built a file at a time with frequent compaction:
// still equal to a full rebuild, settled into day files, and a restart reads
// only the last week's request parts and the unsealed days' ledger.
func TestStartupReadsOnlyRecentParts(t *testing.T) {
	heavy(t)
	t.Parallel() // slow under -race: let it overlap the others
	ctx := context.Background()
	n := 30
	if full {
		n = 60
	}
	c := longCapture(n)
	want, _ := rebuild(t, c)
	raw, out := newMem(), &readLog{memStore: newMem()}
	now := t0
	p := newProc(raw, out, &now, 3, 4)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range c.sorted() {
		put(t, raw, f)
		now = f.at.Add(time.Minute)
		if _, err := p.Poll(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	compareTables(t, readTables(t, out, "aintercept/shaped"), want)

	m := p.man
	newest := time.Unix(0, m.NewestNS).UTC()
	if wantSeal := newest.Add(-sealAfter).Truncate(24 * time.Hour).Add(-24 * time.Hour).Format("2006-01-02"); m.SealedThrough != wantSeal {
		t.Fatalf("sealed through %q, want %q", m.SealedThrough, wantSeal)
	}
	days := 0
	for _, k := range m.Facts["requests"] {
		if m.Parts[k].Day != "" {
			days++
		}
	}
	if days < n-5 {
		t.Fatalf("%d request day files for %d days", days, n)
	}

	out.gets = nil
	p2 := newProc(raw, out, &now, 3, 4)
	if err := p2.Open(ctx); err != nil {
		t.Fatal(err)
	}
	var reqs, ledger []string
	for _, k := range out.gets {
		switch {
		case strings.Contains(k, "/requests/"):
			reqs = append(reqs, k)
		case strings.Contains(k, "/ledger/"):
			ledger = append(ledger, k)
		}
	}
	for _, k := range reqs {
		if info := m.Parts[k]; m.NewestNS-info.MaxNS > int64(pendingTTLWS) {
			t.Fatalf("start read %s, older than any marker", k)
		}
	}
	for _, k := range ledger {
		if d := m.Parts[k].Day; d != "" && d <= m.SealedThrough {
			t.Fatalf("start read sealed ledger %s", k)
		}
	}
	if len(reqs) > 10 || len(ledger) > 6 {
		sort.Strings(reqs)
		t.Fatalf("start read %d request parts and %d ledger parts of %d and %d", len(reqs), len(ledger),
			len(m.Facts["requests"]), len(m.Facts["ledger"]))
	}
	// The restarted processor carries on: nothing to redo, nothing new missed.
	if n, err := p2.Poll(ctx, true); err != nil || n != 0 {
		t.Fatalf("full poll after restart: %d files, %v", n, err)
	}
	compareTables(t, readTables(t, out.memStore, "aintercept/shaped"), want)
}

// A rebuild's first run part holds the whole history; compacting it afterwards
// (a day at a time, so memory holds one day's rows: 2.6 GB before, on the
// cluster's three weeks, docs/INTERCEPTION_ISSUES.md P5) still settles every
// day into its file and matches a full rebuild.
func TestCompactionAfterARebuild(t *testing.T) {
	heavy(t)
	ctx := context.Background()
	c := longCapture(60) // one run: cheap at any size
	want, _ := rebuild(t, c)
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	now := t0.Add(61 * 24 * time.Hour)
	p := newProc(raw, out, &now, 1_000_000, 1) // one run takes everything, and is compacted
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	compareTables(t, readTables(t, out, "aintercept/shaped"), want)
	days := 0
	for _, k := range p.man.Facts["requests"] {
		if p.man.Parts[k].Day != "" {
			days++
		}
	}
	if days != 60 || len(p.man.runParts("requests")) != 0 {
		t.Fatalf("%d day files and %d run parts, want 60 and 0", days, len(p.man.runParts("requests")))
	}
}

// A raw file that lands in a sealed day isn't listed again; a sweep finds it.
func TestSweepFindsAFileInASealedDay(t *testing.T) {
	ctx := context.Background()
	c := longCapture(10)
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	now := t0.Add(30 * 24 * time.Hour)
	p := newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if p.man.SealedThrough == "" {
		t.Fatal("ten days of data should seal some")
	}
	late := &capture{files: map[string]*rawFile{}}
	at := t0.Add(time.Hour) // day one, long sealed
	late.add("obs-c", events.Event{CorrelationID: "obs-c-late", TimestampNS: ns(at), Phase: events.PhaseComplete,
		Host: "api.openai.com", Method: "POST", Path: "/v1/responses", Status: 200})
	for _, f := range late.sorted() {
		put(t, raw, f)
	}
	if n, _ := p.Poll(ctx, true); n != 0 {
		t.Fatalf("a full listing processed %d files from a sealed day", n)
	}
	sweep := New(Config{Raw: raw, RawPrefix: "aintercept", Out: out, OutPrefix: "aintercept/shaped", Now: func() time.Time { return now },
		Wall: func() time.Time { return now }, SweepAll: true})
	if err := sweep.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := sweep.Poll(ctx, true); err != nil || n != 1 {
		t.Fatalf("sweep processed %d, %v; want the late file", n, err)
	}
	got := readTables(t, out, "aintercept/shaped")
	found := false
	for _, r := range got.Requests {
		found = found || r.CallID == "obs-c-late"
	}
	if !found {
		t.Fatal("the late file's request isn't in the tables")
	}
}
