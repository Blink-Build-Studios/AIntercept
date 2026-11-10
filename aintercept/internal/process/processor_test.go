package process

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// --- an in-memory store ----------------------------------------------------------------

type memStore struct {
	mu      sync.Mutex
	objs    map[string][]byte
	failPut func(key string) bool // inject a failure
}

func newMem() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) List(_ context.Context, prefix string) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Object
	for k, v := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memStore) ListDirs(ctx context.Context, prefix string) ([]string, error) {
	objs, _ := m.List(ctx, prefix)
	seen := map[string]bool{}
	var out []string
	for _, o := range objs {
		if i := strings.Index(o.Key[len(prefix):], "/"); i >= 0 {
			d := o.Key[:len(prefix)+i+1]
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (m *memStore) ListAfter(ctx context.Context, prefix, after string) ([]Object, error) {
	objs, _ := m.List(ctx, prefix)
	var out []Object
	for _, o := range objs {
		if o.Key > after && !strings.Contains(o.Key[len(prefix):], "/") {
			out = append(out, o)
		}
	}
	return out, nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

func (m *memStore) Put(_ context.Context, key string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut != nil && m.failPut(key) {
		return errors.New("injected put failure")
	}
	m.objs[key] = append([]byte(nil), b...)
	return nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

// --- a synthetic capture ----------------------------------------------------------------

var t0 = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

// rawFile is one observer upload: its key and rows.
type rawFile struct {
	key  string
	at   time.Time // the batch start the key is named by
	rows []events.Event
}

type capture struct {
	files map[string]*rawFile
}

func (c *capture) add(pod string, e events.Event) {
	at := time.Unix(0, e.TimestampNS).UTC().Truncate(90 * time.Second) // one batch per 90 s per pod
	key := fmt.Sprintf("aintercept/dt=%s/hour=%02d/instance=%s/%d-000001.ndjson.gz",
		at.Format("2006-01-02"), at.Hour(), pod, at.UnixNano())
	f := c.files[key]
	if f == nil {
		f = &rawFile{key: key, at: at}
		c.files[key] = f
	}
	f.rows = append(f.rows, e)
}

func (c *capture) sorted() []*rawFile {
	var out []*rawFile
	for _, f := range c.files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func gz(t *testing.T, rows []events.Event) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		zw.Write(append(b, '\n'))
	}
	zw.Write([]byte("not json\n")) // a bad line is counted, not fatal
	zw.Close()
	return buf.Bytes()
}

func ns(t time.Time) int64 { return t.UnixNano() }

// newCapture builds five Claude Code sessions for three people over 8 hours,
// each call's rows split across batches and pods, with the analytics that
// report on them, plus the transactions that end other ways: a disconnect, a
// WebSocket with two turns, one that never ends, and a reported call that was
// never captured.
func newCapture() *capture {
	c := &capture{files: map[string]*rawFile{}}
	people := []string{"acct-a", "acct-b", "acct-c"}
	for s := 0; s < 5; s++ {
		sid := fmt.Sprintf("sess-%d", s)
		person := people[s%3]
		device := fmt.Sprintf("dev-%d", s%3)
		if s == 4 {
			device = "dev-c2" // acct-b's second device
			person = "acct-b"
		}
		start := t0.Add(time.Duration(s) * 100 * time.Minute)
		for k := 0; k < 12; k++ {
			at := start.Add(time.Duration(k) * 4 * time.Minute)
			cid := fmt.Sprintf("obs-a-%d-%d", s, k)
			pod := []string{"obs-a", "obs-b"}[k%2]
			prompt := fmt.Sprintf("prompt-%d-%d", s, k/3)
			client := map[string]string{
				"x-claude-code-session-id": sid, "x-claude-code-prompt-id": prompt,
				"x-claude-code-request-class": "main", "x-client-request-id": "creq-" + cid, "user-agent": "claude-cli/2.1.308",
			}
			reqID := fmt.Sprintf("req_%d_%d", s, k)
			c.add(pod, events.Event{CorrelationID: cid, Seq: 1, TimestampNS: ns(at), Phase: events.PhaseRequestHeaders,
				Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages?beta=true", KeyHash: "kh-" + person, IP: "10.0.0.1",
				Headers: map[string]string{"x-claude-code-session-id": sid, "user-agent": "claude-cli/2.1.308"}})
			c.add(pod, events.Event{CorrelationID: cid, Seq: 2, TimestampNS: ns(at.Add(800 * time.Millisecond)), Phase: events.PhaseResponseHeaders,
				Host: "api.anthropic.com", Status: 200, Headers: map[string]string{"request-id": reqID}})
			end := at.Add(time.Duration(5+k*10) * time.Second) // later calls cross into the next batch
			c.add(pod, events.Event{CorrelationID: cid, Seq: 3, TimestampNS: ns(end), Phase: events.PhaseComplete,
				Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages?beta=true", Status: 200, KeyHash: "kh-" + person, IP: "10.0.0.1",
				RequestStartNS: ns(at), ResponseStartNS: ns(at.Add(800 * time.Millisecond)), RequestBytes: 9000 + k, ResponseBytes: 400 + k,
				Model: "claude-opus-5-5", StopReason: "end_turn", InputTokens: 100 + k, OutputTokens: 10 + k, CacheReadTokens: 50,
				AccountUUID: person, DeviceID: device, SessionID: sid, Client: client, Provider: map[string]string{"request-id": reqID},
				ToolUses: []events.ToolCallCount{{Name: "Bash", Count: 1}}})

			if k == 5 { // the stream errors after completing: the disconnect adds nothing
				c.add(pod, events.Event{CorrelationID: cid, Seq: 4, TimestampNS: ns(end.Add(2 * time.Minute)), Phase: events.PhaseDisconnect,
					Error: "stream reset"})
			}
			// The analytics batch reporting on it, a little later, on the other pod.
			acid := fmt.Sprintf("obs-b-an-%d-%d", s, k)
			rat := end.Add(20 * time.Second)
			c.add("obs-b", events.Event{CorrelationID: acid, Seq: 1, TimestampNS: ns(rat), Phase: events.PhaseRequestHeaders,
				Host: "api.anthropic.com", Method: "POST", Path: "/api/event_logging/v2/batch"})
			c.add("obs-b", events.Event{CorrelationID: acid, Seq: 2, TimestampNS: ns(rat.Add(time.Second)), Phase: events.PhaseComplete,
				Host: "api.anthropic.com", Method: "POST", Path: "/api/event_logging/v2/batch", Status: 200,
				RequestStartNS: ns(rat), ResponseStartNS: ns(rat.Add(500 * time.Millisecond))})
			for i, name := range []string{"tengu_input_prompt", "tengu_api_success"} {
				if name == "tengu_input_prompt" && k%3 != 0 {
					continue
				}
				md := map[string]any{"cc_prompt_id": prompt, "permissionMode": "bypassPermissions"}
				if name == "tengu_api_success" {
					md["requestId"], md["clientRequestId"] = reqID, "creq-"+cid
				} else {
					md["prompt_index"] = float64(k/3 + 1)
				}
				c.add("obs-b", events.Event{CorrelationID: acid, Seq: 2, TimestampNS: ns(rat.Add(time.Second)), Phase: events.PhaseClientEvent,
					Host: "api.anthropic.com", ClientEvent: &events.ClientEvent{Source: "claude_code", Name: name,
						ID: fmt.Sprintf("ev-%s-%d", acid, i), Timestamp: at.Add(-time.Second).Format(time.RFC3339Nano),
						SessionID: sid, AccountUUID: person, OrganizationUUID: "org-1", DeviceID: device,
						Data: map[string]any{"additional_metadata": md, "env": map[string]any{"platform": "darwin"}}}})
			}
		}
		// A call the client reported that the proxy never saw: a coverage gap.
		gapAt := start.Add(30 * time.Minute)
		c.add("obs-a", events.Event{CorrelationID: "obs-a-gap-" + sid, Seq: 1, TimestampNS: ns(gapAt), Phase: events.PhaseClientEvent,
			Host: "api.anthropic.com", ClientEvent: &events.ClientEvent{Source: "claude_code", Name: "tengu_api_success",
				SessionID: sid, AccountUUID: person, DeviceID: device,
				Data: map[string]any{"additional_metadata": map[string]any{"requestId": "req_never_" + sid}}}})
	}
	// A request the client dropped mid-response.
	dat := t0.Add(2 * time.Hour)
	c.add("obs-a", events.Event{CorrelationID: "obs-a-drop", Seq: 1, TimestampNS: ns(dat), Phase: events.PhaseRequestHeaders,
		Host: "chatgpt.com", Method: "POST", Path: "/backend-api/conversation"})
	c.add("obs-a", events.Event{CorrelationID: "obs-a-drop", Seq: 2, TimestampNS: ns(dat.Add(3 * time.Minute)), Phase: events.PhaseDisconnect,
		Error: "context canceled"})
	// A WebSocket with two model turns, then its close.
	wat := t0.Add(3 * time.Hour)
	c.add("obs-b", events.Event{CorrelationID: "obs-b-ws", Seq: 1, TimestampNS: ns(wat), Phase: events.PhaseRequestHeaders,
		Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses", Headers: map[string]string{"upgrade": "websocket"}})
	for turn := 1; turn <= 2; turn++ {
		c.add("obs-b", events.Event{CorrelationID: "obs-b-ws", Seq: uint64(1 + turn), TimestampNS: ns(wat.Add(time.Duration(turn) * 10 * time.Minute)),
			Phase: events.PhaseComplete, Host: "chatgpt.com", Transport: events.TransportWebSocket, WSTurn: turn, Model: "gpt-6.1-sol", OutputTokens: 7})
	}
	c.add("obs-b", events.Event{CorrelationID: "obs-b-ws", Seq: 9, TimestampNS: ns(wat.Add(40 * time.Minute)), Phase: events.PhaseWSConnection,
		Host: "chatgpt.com", Transport: events.TransportWebSocket})
	// A request whose ending never arrives (its observer died): written once it expires.
	c.add("obs-a", events.Event{CorrelationID: "obs-a-lost", Seq: 1, TimestampNS: ns(t0.Add(10 * time.Minute)), Phase: events.PhaseRequestHeaders,
		Host: "api.openai.com", Method: "POST", Path: "/v1/responses"})
	return c
}

// --- reading the result -----------------------------------------------------------------

type tables = Tables

func readTables(t *testing.T, s Store, prefix string) tables {
	t.Helper()
	out, err := ReadTables(context.Background(), s, prefix)
	if err != nil {
		t.Fatal(err)
	}
	for i := range out.Ledger {
		out.Ledger[i].Run = 0 // which run took a file differs between schedules
	}
	return out
}

func readAll[T any](t *testing.T, s Store, keys []string) []T {
	t.Helper()
	var all []T
	for _, k := range keys {
		if k == "" {
			continue
		}
		rows, err := readParquet[T](context.Background(), s, k)
		if err != nil {
			t.Fatalf("read %s: %v", k, err)
		}
		all = append(all, rows...)
	}
	return all
}

func put(t *testing.T, s Store, f *rawFile) {
	t.Helper()
	if err := s.Put(context.Background(), f.key, gz(t, f.rows)); err != nil {
		t.Fatal(err)
	}
}

func newProc(raw, out Store, now *time.Time, batchFiles, compactAt int) *Processor {
	return New(Config{Raw: raw, RawPrefix: "aintercept", Out: out, OutPrefix: "aintercept/shaped",
		BatchFiles: batchFiles, CompactAt: compactAt, Grace: time.Minute, Now: func() time.Time { return *now }, Wall: func() time.Time { return *now }})
}

// rebuild processes every file in one full pass.
func rebuild(t *testing.T, c *capture) (tables, *memStore) {
	t.Helper()
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	now := t0.Add(24 * time.Hour)
	p := newProc(raw, out, &now, 10000, 1000)
	if err := p.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return readTables(t, out, "aintercept/shaped"), out
}

// --- tests --------------------------------------------------------------------------------

// Arriving bit by bit, out of order, across restarts and compactions, the
// tables come out exactly as one full rebuild makes them.
func TestIncrementalMatchesRebuild(t *testing.T) {
	c := newCapture()
	want, _ := rebuild(t, c)
	if len(want.Requests) == 0 || len(want.Sessions) != 5 {
		t.Fatalf("rebuild made %d requests, %d sessions", len(want.Requests), len(want.Sessions))
	}
	files := c.sorted()
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			ctx := context.Background()
			rng := rand.New(rand.NewSource(seed))
			raw, out := newMem(), newMem()
			now := t0
			p := newProc(raw, out, &now, 1+rng.Intn(4), 4) // tiny runs, frequent compaction
			if err := p.Open(ctx); err != nil {
				t.Fatal(err)
			}
			// Each pod uploads its batches in order, one at a time (the observer's
			// upload loop), each landing up to five batches late; pods interleave.
			order := append([]*rawFile(nil), files...)
			lands := map[*rawFile]time.Time{}
			last := map[string]time.Time{}
			for _, f := range order { // files are in key order: per pod, in batch order
				pod := f.key[strings.Index(f.key, "instance="):strings.LastIndex(f.key, "/")]
				at := f.at.Add(time.Duration(rng.Intn(6)) * 90 * time.Second)
				if at.Before(last[pod]) {
					at = last[pod]
				}
				lands[f], last[pod] = at, at
			}
			sort.SliceStable(order, func(i, j int) bool { return lands[order[i]].Before(lands[order[j]]) })
			// One file is held back for hours: only the full listing finds it.
			late := order[len(order)/3]
			for _, f := range order {
				if f == late {
					continue
				}
				put(t, raw, f)
				if f.at.After(now) {
					now = f.at.Add(time.Minute)
				}
				if rng.Intn(2) == 0 {
					if _, err := p.Poll(ctx, false); err != nil {
						t.Fatal(err)
					}
				}
				if rng.Intn(15) == 0 { // restart: a new processor resumes from the commit
					p = newProc(raw, out, &now, 1+rng.Intn(4), 4)
					if err := p.Open(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			put(t, raw, late)
			now = now.Add(2 * time.Hour)
			if _, err := p.Poll(ctx, false); err != nil {
				t.Fatal(err)
			}
			if _, ok := p.seen[late.key]; ok {
				t.Fatal("a recent-hours poll shouldn't reach a file from hours ago")
			}
			if _, err := p.Poll(ctx, true); err != nil {
				t.Fatal(err)
			}
			got := readTables(t, out, "aintercept/shaped")
			compareTables(t, got, want)
		})
	}
}

func compareTables(t *testing.T, got, want tables) {
	t.Helper()
	// first_file is best effort across a file found late (see Request.FirstFile).
	got.Requests, want.Requests = withoutFirstFile(got.Requests), withoutFirstFile(want.Requests)
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < gv.NumField(); i++ {
		name := gv.Type().Field(i).Name
		g, w := gv.Field(i).Interface(), wv.Field(i).Interface()
		if !reflect.DeepEqual(g, w) {
			if gr, ok := g.([]Request); ok {
				wr := w.([]Request)
				for k := 0; k < len(gr) && k < len(wr); k++ {
					if !reflect.DeepEqual(gr[k], wr[k]) {
						t.Logf("first differing request:\n got %+v\nwant %+v", gr[k], wr[k])
						break
					}
				}
				t.Logf("got %d, want %d requests", len(gr), len(wr))
			}
			gb, _ := json.MarshalIndent(g, "", " ")
			wb, _ := json.MarshalIndent(w, "", " ")
			t.Fatalf("%s differ:\n got %d bytes: %.3000s\nwant %d bytes: %.3000s", name, len(gb), gb, len(wb), wb)
		}
	}
}

// The tables hold what the capture says: every call once, sessions with their
// counts, the transactions that ended other ways.
func TestTablesContent(t *testing.T) {
	got, _ := rebuild(t, newCapture())
	byID := map[string]Request{}
	for _, r := range got.Requests {
		byID[r.CallID] = r
	}
	// 60 model calls and their 60 analytics batches, the dropped request, two
	// WebSocket turns and the connection, and the request that never ended.
	if n := len(got.Requests); n != 60+60+1+3+1 {
		t.Fatalf("requests = %d", n)
	}
	c := byID["obs-a-0-3"]
	if c.SessionID != "sess-0" || c.PromptID != "prompt-0-1" || c.RequestID != "req_0_3" || c.PersonID != "acct-a" ||
		c.StartNS != ns(t0.Add(12*time.Minute)) || c.RequestBytes != 9003 || c.OutputTokens != 13 || c.ToolUsesJSON == "" {
		t.Fatalf("call row: %+v", c)
	}
	if d := byID["obs-a-drop"]; d.Error != "context canceled" || d.Host != "chatgpt.com" || d.EndNS == 0 {
		t.Fatalf("dropped request: %+v", d)
	}
	if byID["obs-b-ws#1"].Model != "gpt-6.1-sol" || byID["obs-b-ws#2"].WSTurn != 2 || byID["obs-b-ws"].Transport != events.TransportWebSocket {
		t.Fatal("WebSocket turns and connection rows")
	}
	if l := byID["obs-a-lost"]; l.Host != "api.openai.com" || l.EndNS != 0 {
		t.Fatalf("the request that never ended is written without an end: %+v", l)
	}
	s0 := got.Sessions[0]
	if s0.SessionID != "sess-0" || s0.Calls != 12 || s0.Prompts != 4 || s0.PersonID != "acct-a" || s0.OrgID != "org-1" ||
		s0.Events != 12+4+1 || s0.CallsWithUsage != 12 || s0.OutputTokens != 12*10+66 {
		t.Fatalf("session: %+v", s0)
	}
	var b Person
	for _, p := range got.People {
		if p.PersonID == "acct-b" {
			b = p
		}
	}
	if b.Sessions != 2 || b.Devices != 2 || b.Calls != 24 {
		t.Fatalf("acct-b: %+v", b)
	}
	for _, l := range got.Ledger {
		if l.Bad != 1 || l.Rows == 0 {
			t.Fatalf("ledger counts each file's rows and its bad line: %+v", l)
		}
	}
}

// Every primary key is unique and every non-null reference resolves. The only
// unresolved references are the reported calls the proxy never captured.
func TestKeysResolve(t *testing.T) {
	got, _ := rebuild(t, newCapture())
	unique := func(name string, keys []string) map[string]bool {
		set := map[string]bool{}
		for _, k := range keys {
			if set[k] {
				t.Fatalf("%s: duplicate key %q", name, k)
			}
			set[k] = true
		}
		return set
	}
	calls := unique("requests", mapf(got.Requests, func(r Request) string { return r.CallID }))
	sessions := unique("sessions", mapf(got.Sessions, func(s Session) string { return s.SessionID }))
	prompts := unique("prompts", mapf(got.Prompts, func(p Prompt) string { return p.PromptID }))
	people := unique("people", mapf(got.People, func(p Person) string { return p.PersonID }))
	unique("events", mapf(got.Events, func(e Event) string { return e.EventKey }))
	unique("person_devices", mapf(got.Devices, func(d PersonDevice) string { return d.PersonID + "|" + d.DeviceID }))
	unique("ledger", mapf(got.Ledger, func(l Ledger) string { return l.RawKey }))
	requestIDs := map[string]bool{}
	for _, r := range got.Requests {
		if r.RequestID != "" {
			if requestIDs[r.RequestID] {
				t.Fatalf("request_id %q twice", r.RequestID)
			}
			requestIDs[r.RequestID] = true
		}
	}
	ref := func(what, key string, set map[string]bool) {
		if key != "" && !set[key] {
			t.Fatalf("%s %q resolves to nothing", what, key)
		}
	}
	for _, r := range got.Requests {
		if isModelCall(&r) {
			ref("requests.session_id", r.SessionID, sessions)
			ref("requests.prompt_id", r.PromptID, prompts)
			ref("requests.person_id", r.PersonID, people)
		}
	}
	gaps := 0
	for _, e := range got.Events {
		ref("events.correlation_id", e.CorrelationID, map[string]bool{e.CorrelationID: calls[e.CorrelationID] || strings.HasPrefix(e.CorrelationID, "obs-a-gap-")})
		ref("events.session_id", e.SessionID, sessions)
		ref("events.prompt_id", e.PromptID, prompts)
		ref("events.person_id", e.PersonID, people)
		if e.RequestID != "" && !requestIDs[e.RequestID] {
			gaps++
		}
	}
	if gaps != 5 {
		t.Fatalf("unresolved request_id: %d, want the 5 never-captured calls", gaps)
	}
	// The shared check the replay tool uses agrees.
	if k := CheckKeys(got); len(k.Problems) != 0 || k.UncapturedCalls != 5 {
		t.Fatalf("CheckKeys: %+v", k)
	}
	if d := Diff(got, got, 5); len(d) != 0 {
		t.Fatalf("Diff of a table with itself: %v", d)
	}
	for _, s := range got.Sessions {
		ref("sessions.person_id", s.PersonID, people)
	}
	for _, p := range got.Prompts {
		ref("prompts.session_id", p.SessionID, sessions)
	}
	for _, d := range got.Devices {
		ref("person_devices.person_id", d.PersonID, people)
	}
}

// A session grows as its files arrive: each poll's tables are what the files
// so far say, and the session's row is rewritten, not duplicated.
func TestSessionsGrowAsDataArrives(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	raw, out := newMem(), newMem()
	now := t0
	p := newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	var lastCalls, lastNS int64
	grew := 0
	for _, f := range c.sorted() {
		if !strings.Contains(f.key, "/hour=09/") { // sess-0's first hour
			continue
		}
		put(t, raw, f)
		now = f.at.Add(time.Minute)
		if _, err := p.Poll(ctx, false); err != nil {
			t.Fatal(err)
		}
		got := readTables(t, out, "aintercept/shaped")
		var s0 *Session
		for i := range got.Sessions {
			if got.Sessions[i].SessionID == "sess-0" {
				if s0 != nil {
					t.Fatal("sess-0 twice")
				}
				s0 = &got.Sessions[i]
			}
		}
		if s0 == nil {
			continue
		}
		calls := int64(0)
		for _, r := range got.Requests {
			if r.SessionID == "sess-0" && isModelCall(&r) {
				calls++
			}
		}
		if s0.Calls != calls {
			t.Fatalf("session says %d calls, the requests table has %d", s0.Calls, calls)
		}
		if s0.Calls < lastCalls || s0.LastNS < lastNS {
			t.Fatal("a session only grows")
		}
		if s0.Calls > lastCalls {
			grew++
		}
		lastCalls, lastNS = s0.Calls, s0.LastNS
	}
	if grew < 3 || lastCalls != 12 {
		t.Fatalf("grew %d times to %d calls", grew, lastCalls)
	}
}

// A run that dies before its commit leaves nothing readers see; the next
// start removes its files and does the work again.
func TestCrashBeforeCommitIsRedone(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	want, _ := rebuild(t, c)
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	now := t0.Add(24 * time.Hour)
	p := newProc(raw, out, &now, 7, 1000)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	commits := 0
	out.failPut = func(key string) bool {
		if strings.HasSuffix(key, "manifest.json") {
			commits++
			return commits == 3 // the third run's commit fails
		}
		return false
	}
	if _, err := p.Poll(ctx, true); err == nil {
		t.Fatal("want the injected failure")
	}
	out.failPut = nil
	orphans := 0
	for k := range out.objs {
		if strings.Contains(k, "run-000000000003") {
			orphans++
		}
	}
	if orphans == 0 {
		t.Fatal("the failed run should have left files behind")
	}
	p = newProc(raw, out, &now, 7, 1000)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	for k := range out.objs {
		if strings.Contains(k, "run-000000000003") {
			t.Fatalf("orphan %s not removed", k)
		}
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	compareTables(t, readTables(t, out, "aintercept/shaped"), want)
}

// The same processor retrying after a failed write (a brief S3 error) must not
// reapply the failed run's rows on top of the state they already changed: that
// lost the run's requests (their end markers were set) and counted twice.
func TestFailedWriteIsRetriedInPlace(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	want, _ := rebuild(t, c)
	for _, failOn := range []string{"manifest.json", "/sessions/", "/requests/"} {
		t.Run(failOn, func(t *testing.T) {
			raw, out := newMem(), newMem()
			for _, f := range c.sorted() {
				put(t, raw, f)
			}
			now := t0.Add(24 * time.Hour)
			p := newProc(raw, out, &now, 7, 1000)
			if err := p.Open(ctx); err != nil {
				t.Fatal(err)
			}
			n := 0
			out.failPut = func(key string) bool {
				if strings.Contains(key, failOn) {
					n++
					return n == 2 // the second run's write fails
				}
				return false
			}
			if _, err := p.Poll(ctx, true); err == nil {
				t.Fatal("want the injected failure")
			}
			out.failPut = nil
			if _, err := p.Poll(ctx, false); err != nil {
				t.Fatal(err)
			}
			compareTables(t, readTables(t, out, "aintercept/shaped"), want)
		})
	}
}

// An unreadable file stays unread across a restart: listed in /stats, keeping
// its day from being sealed, and retried at the next listing.
func TestUnreadSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	f := c.sorted()[0]
	raw, out := newMem(), newMem()
	good := gz(t, f.rows)
	raw.Put(ctx, f.key, good[:len(good)/2])
	now := f.at.Add(time.Minute)
	p := newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	p = newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.unread[f.key]; !ok {
		t.Fatal("unread file forgotten at restart")
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := p.Stats().FilesUnread; len(got) != 1 || got[0] != f.key {
		t.Fatalf("unread after restart: %v", got)
	}
	if d := p.sealThrough(&p.man); d >= dayOf(f.at.UnixNano()) {
		t.Fatalf("sealed through %s, past the unread file's day", d)
	}
}

// A file that can't be read yet (an upload still landing, a truncated copy)
// isn't marked processed; it's retried once it changes.
func TestUnreadableFileIsRetried(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	f := c.sorted()[0]
	raw, out := newMem(), newMem()
	good := gz(t, f.rows)
	raw.Put(ctx, f.key, good[:len(good)/2])
	now := f.at.Add(time.Minute)
	p := newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.seen[f.key]; ok || len(p.Stats().FilesUnread) != 1 {
		t.Fatalf("truncated file: seen=%v unread=%v", ok, p.Stats().FilesUnread)
	}
	if n, _ := p.Poll(ctx, false); n != 0 {
		t.Fatal("an unchanged unreadable file isn't retried every poll")
	}
	raw.Put(ctx, f.key, good)
	if n, _ := p.Poll(ctx, false); n != 1 {
		t.Fatalf("processed %d after the file was fixed", n)
	}
	if len(p.Stats().FilesUnread) != 0 {
		t.Fatal("fixed file still listed unread")
	}
}

// Rows from before the observer put timing and sizes on `complete` get them
// from the call's header and body rows instead.
func TestOlderRowsTakeTimingFromHeaders(t *testing.T) {
	c := &capture{files: map[string]*rawFile{}}
	at := t0
	c.add("obs-a", events.Event{CorrelationID: "old", TimestampNS: ns(at), Phase: events.PhaseRequestHeaders,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages",
		Headers: map[string]string{"x-claude-code-session-id": "sess-old", "x-claude-code-prompt-id": "p-old"}})
	c.add("obs-a", events.Event{CorrelationID: "old", TimestampNS: ns(at.Add(time.Second)), Phase: events.PhaseRequestBodyChunk, Bytes: 700})
	c.add("obs-a", events.Event{CorrelationID: "old", TimestampNS: ns(at.Add(2 * time.Second)), Phase: events.PhaseResponseHeaders,
		Status: 200, Headers: map[string]string{"request-id": "req_old"}})
	c.add("obs-a", events.Event{CorrelationID: "old", TimestampNS: ns(at.Add(3 * time.Second)), Phase: events.PhaseResponseBodyChunk, Bytes: 50})
	c.add("obs-a", events.Event{CorrelationID: "old", TimestampNS: ns(at.Add(3 * time.Second)), Phase: events.PhaseResponseBodyChunk, Bytes: 25})
	c.add("obs-a", events.Event{CorrelationID: "old", TimestampNS: ns(at.Add(4 * time.Second)), Phase: events.PhaseComplete,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Status: 200, OutputTokens: 5})
	got, _ := rebuild(t, c)
	r := got.Requests[0]
	if r.StartNS != ns(at) || r.ResponseStartNS != ns(at.Add(2*time.Second)) || r.RequestBytes != 700 || r.ResponseBytes != 75 ||
		r.RequestID != "req_old" || r.SessionID != "sess-old" || r.PromptID != "p-old" {
		t.Fatalf("older row: %+v", r)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Calls != 1 {
		t.Fatalf("sessions: %+v", got.Sessions)
	}
}

// A call's header rows arriving after its complete row (a later file, a later
// run) don't open a transaction that later expires into a second row.
func TestLateHeaderRowsAreNotWrittenTwice(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	want, _ := rebuild(t, c)
	hasComplete := func(f *rawFile) bool {
		for _, r := range f.rows {
			if r.Phase == events.PhaseComplete {
				return true
			}
		}
		return false
	}
	raw, out := newMem(), newMem()
	now := t0.Add(24 * time.Hour)
	p := newProc(raw, out, &now, 3, 8)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	// Hour by hour, every file holding a complete row lands first, then the
	// rest, one poll each: header rows up to an hour late, well inside the
	// window the end markers cover.
	hours := map[string][]*rawFile{}
	var order []string
	for _, f := range c.sorted() {
		h := f.at.Format("2006-01-02T15")
		if hours[h] == nil {
			order = append(order, h)
		}
		hours[h] = append(hours[h], f)
	}
	sort.Strings(order)
	for _, h := range order {
		for _, first := range []bool{true, false} {
			for _, f := range hours[h] {
				if hasComplete(f) == first {
					put(t, raw, f)
					if _, err := p.Poll(ctx, true); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	got := readTables(t, out, "aintercept/shaped")
	seen := map[string]bool{}
	for _, r := range got.Requests {
		if seen[r.CallID] {
			t.Fatalf("%s written twice", r.CallID)
		}
		seen[r.CallID] = true
	}
	if len(got.Requests) != len(want.Requests) {
		t.Fatalf("requests %d, rebuild %d", len(got.Requests), len(want.Requests))
	}
	// Complete rows describe their calls fully, so sessions don't depend on the order.
	if !reflect.DeepEqual(got.Sessions, want.Sessions) {
		t.Fatal("sessions differ from the rebuild")
	}
}

// Replaced files stay readable for the grace period, then go.
func TestReplacedFilesAreCollectedAfterGrace(t *testing.T) {
	ctx := context.Background()
	c := newCapture()
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	now := t0.Add(24 * time.Hour)
	p := newProc(raw, out, &now, 5, 4)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(p.man.Garbage) == 0 {
		t.Fatal("snapshots and compacted parts should be waiting out their grace period")
	}
	count := func() int { n, _ := out.List(ctx, "aintercept/shaped/"); return len(n) }
	before := count()
	now = now.Add(2 * time.Minute)
	if _, err := p.Poll(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(p.man.Garbage) != 0 || count() >= before {
		t.Fatalf("garbage left: %d, files %d -> %d", len(p.man.Garbage), before, count())
	}
	named := 1 // the manifest
	for _, ks := range p.man.Facts {
		named += len(ks)
	}
	named += len(p.man.Snapshots)
	if count() != named {
		t.Fatalf("%d files, the manifest names %d", count(), named)
	}
}

func withoutFirstFile(rs []Request) []Request {
	out := append([]Request(nil), rs...)
	for i := range out {
		out[i].FirstFile = ""
	}
	return out
}

func mapf[T any](xs []T, f func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}

// Who an account is (Claude Code's account endpoints) lands in the accounts snapshot:
// the latest sighting wins, whatever order the files arrive in, and one that names no
// one (bootstrap: an email only) keeps the name already known.
func TestAccountsSnapshot(t *testing.T) {
	ctx := context.Background()
	c := &capture{files: map[string]*rawFile{}}
	at := func(m int) int64 { return ns(t0.Add(time.Duration(m) * time.Minute)) }
	sight := func(id string, m int, auth events.AuthIdentity) {
		c.add("pod-a", events.Event{CorrelationID: id, Phase: events.PhaseRequestHeaders, TimestampNS: at(m), Host: "api.anthropic.com", Method: "GET", Path: "/api/oauth/profile"})
		c.add("pod-a", events.Event{CorrelationID: id, Phase: events.PhaseComplete, TimestampNS: at(m) + 1, Status: 200, Auth: &auth})
	}
	sight("p1", 1, events.AuthIdentity{AccountID: "acct-a", Email: "old@example.com", Name: "Old Name"})
	sight("p2", 20, events.AuthIdentity{AccountID: "acct-a", Email: "a@example.com", Name: "Ada Lovelace", Org: "Example Co"})
	sight("p3", 40, events.AuthIdentity{AccountID: "acct-a", Email: "a@example.com"})
	raw, out := newMem(), newMem()
	// Newest file first: the order of arrival mustn't matter.
	files := c.sorted()
	for i := len(files) - 1; i >= 0; i-- {
		put(t, raw, files[i])
	}
	now := t0.Add(2 * time.Hour)
	p := newProc(raw, out, &now, 1, 1000)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	got := readAll[Account](t, out, []string{p.man.Snapshots["accounts"]})
	if len(got) != 1 || got[0].Email != "a@example.com" || got[0].Name != "Ada Lovelace" || got[0].Org != "Example Co" {
		t.Fatalf("accounts: %+v", got)
	}
	// And it survives a restart.
	p = newProc(raw, out, &now, 1, 1000)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if a := p.st.accounts["acct-a"]; a == nil || a.Name != "Ada Lovelace" {
		t.Fatalf("after restart: %+v", a)
	}
}
