package identity

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

const profileBody = `{"account":{"uuid":"acc-1","email":"a@example.com","full_name":"Ada Example"},"organization":{"uuid":"org-1","name":"Example Co"}}`

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// provider is a fake profile endpoint that counts its requests, answers with
// status (200: profileBody) and can be held until release closes.
type provider struct {
	*httptest.Server
	n       atomic.Int32
	status  atomic.Int32
	release chan struct{}
}

func newProvider(t *testing.T) *provider {
	p := &provider{}
	p.status.Store(200)
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.n.Add(1)
		if p.release != nil {
			<-p.release
		}
		if s := int(p.status.Load()); s != 200 {
			w.WriteHeader(s)
			return
		}
		_, _ = w.Write([]byte(profileBody))
	}))
	t.Cleanup(p.Close)
	return p
}

var oauthHdrs = map[string]string{"authorization": "Bearer " + oauthTok}

func newCache(p *provider, clk *clock, learned func(Learned)) *Cache {
	cfg := Config{Lookup: true, Origin: p.URL, Client: p.Client(), Learned: learned}
	if clk != nil {
		cfg.Now = clk.now
	}
	return New(cfg)
}

func TestLookupFirstSightAndCache(t *testing.T) {
	p := newProvider(t)
	var got []Learned
	var mu sync.Mutex
	c := newCache(p, nil, func(l Learned) { mu.Lock(); got = append(got, l); mu.Unlock() })
	pend := c.Begin("kh-1", "sk-ant…0001", "api.anthropic.com", oauthHdrs)
	if pend == nil || !pend.Wait(2*time.Second) {
		t.Fatal("no lookup, or it didn't end")
	}
	k, ok := c.Get("kh-1")
	if !ok || k.Email != "a@example.com" || k.AccountID != "acc-1" || k.OrgID != "org-1" || k.How != "lookup" {
		t.Fatalf("cached %+v %v", k, ok)
	}
	if a, ok := c.ByAccount("acc-1"); !ok || a.Email != "a@example.com" {
		t.Errorf("by account %+v %v", a, ok)
	}
	if c.Begin("kh-1", "", "api.anthropic.com", oauthHdrs) != nil || p.n.Load() != 1 {
		t.Errorf("a known credential was looked up again (%d requests)", p.n.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].KeyHash != "kh-1" || got[0].KeyHint != "sk-ant…0001" || got[0].How != "lookup" || got[0].Email != "a@example.com" {
		t.Errorf("learned %+v", got)
	}
}

// Concurrent calls with the same new token share one lookup.
func TestSingleflight(t *testing.T) {
	p := newProvider(t)
	p.release = make(chan struct{})
	c := newCache(p, nil, nil)
	var pends []*Pending
	for range 10 {
		pends = append(pends, c.Begin("kh-1", "", "api.anthropic.com", oauthHdrs))
	}
	for _, x := range pends[1:] {
		if x != pends[0] {
			t.Fatal("a second lookup started for the same credential")
		}
	}
	close(p.release)
	pends[0].Wait(2 * time.Second)
	if n := p.n.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// MaxInFlight and the rate limit: a lookup that can't start is a miss, and
// the credential's next call tries again.
func TestLimits(t *testing.T) {
	p := newProvider(t)
	p.release = make(chan struct{})
	clk := &clock{t: time.Unix(1e9, 0)}
	c := New(Config{Lookup: true, Origin: p.URL, Client: p.Client(), MaxInFlight: 2, RatePerSecond: 1, Burst: 3, Now: clk.now})
	a := c.Begin("kh-a", "", "api.anthropic.com", oauthHdrs)
	b := c.Begin("kh-b", "", "api.anthropic.com", oauthHdrs)
	if a == nil || b == nil {
		t.Fatal("first two didn't start")
	}
	if c.Begin("kh-c", "", "api.anthropic.com", oauthHdrs) != nil {
		t.Error("a third started past MaxInFlight 2")
	}
	close(p.release)
	a.Wait(2 * time.Second)
	b.Wait(2 * time.Second)
	waitFor(t, func() bool { return c.Stats().InFlight == 0 })
	// One token left of the burst of 3, then none until a second passes.
	if c.Begin("kh-c", "", "api.anthropic.com", oauthHdrs) == nil {
		t.Error("kh-c didn't start on its next call")
	}
	if c.Begin("kh-d", "", "api.anthropic.com", oauthHdrs) != nil {
		t.Error("kh-d started past the rate limit")
	}
	clk.add(time.Second)
	if c.Begin("kh-d", "", "api.anthropic.com", oauthHdrs) == nil {
		t.Error("kh-d didn't start once the bucket refilled")
	}
	if n := c.Stats().Lookups["ratelimited"]; n != 2 {
		t.Errorf("ratelimited %d, want 2", n)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// A refusal is kept NegativeTTL, a failure ErrorTTL, an answer TTL; past it,
// the credential's next call looks it up again (no background re-verify).
func TestTTLs(t *testing.T) {
	p := newProvider(t)
	clk := &clock{t: time.Unix(1e9, 0)}
	c := newCache(p, clk, nil)
	lookup := func(key string) bool {
		pend := c.Begin(key, "", "api.anthropic.com", oauthHdrs)
		if pend != nil {
			pend.Wait(2 * time.Second)
		}
		return pend != nil
	}

	p.status.Store(401)
	lookup("kh-401")
	clk.add(59 * time.Minute)
	if lookup("kh-401") {
		t.Error("a 401 was looked up again within the negative TTL")
	}
	clk.add(2 * time.Minute)
	if !lookup("kh-401") {
		t.Error("a 401 wasn't looked up again after the negative TTL")
	}

	p.status.Store(503)
	lookup("kh-503")
	clk.add(30 * time.Second)
	if lookup("kh-503") {
		t.Error("a 503 was looked up again within the error TTL")
	}
	clk.add(31 * time.Second)
	p.status.Store(200)
	if !lookup("kh-503") {
		t.Error("a 503 wasn't looked up again after the error TTL")
	}
	if _, ok := c.Get("kh-503"); !ok {
		t.Error("the retry's answer wasn't kept")
	}

	clk.add(23 * time.Hour)
	if _, ok := c.Get("kh-503"); !ok || lookup("kh-503") {
		t.Error("an answer went before 24 h")
	}
	clk.add(2 * time.Hour)
	if _, ok := c.Get("kh-503"); ok {
		t.Error("an answer outlived 24 h")
	}
	n := p.n.Load()
	if !lookup("kh-503") || p.n.Load() != n+1 {
		t.Error("an expired answer wasn't looked up again on its next call")
	}
}

// Resolution order: the credential's confirmed owner, else the claims, else
// an account seen before; the client's claims never override a confirmed
// owner.
func TestResolve(t *testing.T) {
	p := newProvider(t)
	c := newCache(p, nil, nil)
	c.Begin("kh-1", "", "api.anthropic.com", oauthHdrs).Wait(2 * time.Second)

	got := c.Resolve(Call{KeyHash: "kh-1", Reported: "acc-1", OrgID: "org-from-header",
		Claims: &events.AuthIdentity{Email: "claimed@example.com"}})
	if got != (Identity{Email: "a@example.com", Name: "Ada Example", OrgID: "org-1", Source: SourceCredential, Reported: "acc-1", Verified: "acc-1"}) {
		t.Errorf("credential: %+v", got)
	}
	// Spoofing: a body account other than the verified one.
	got = c.Resolve(Call{KeyHash: "kh-1", Reported: "acc-victim"})
	if !got.Mismatch || got.Email != "a@example.com" || got.Verified != "acc-1" || got.Reported != "acc-victim" {
		t.Errorf("spoofed: %+v", got)
	}
	got = c.Resolve(Call{KeyHash: "kh-2", Claims: &events.AuthIdentity{Email: "dev@example.com", Name: "Dev"}, Reported: "acc-1"})
	if got.Source != SourceClaims || got.Email != "dev@example.com" || got.Verified != "" || got.Mismatch {
		t.Errorf("claims: %+v", got)
	}
	got = c.Resolve(Call{KeyHash: "kh-2", Reported: "acc-1"})
	if got.Source != SourceAccount || got.Email != "a@example.com" || got.Verified != "" {
		t.Errorf("account: %+v", got)
	}
	if got = c.Resolve(Call{KeyHash: "kh-2", Reported: "acc-unknown"}); got.Source != SourceNone || got.Email != "" {
		t.Errorf("none: %+v", got)
	}
	var nilCache *Cache
	if got = nilCache.Resolve(Call{KeyHash: "kh-1", Claims: &events.AuthIdentity{Email: "x@example.com"}}); got.Source != SourceClaims {
		t.Errorf("nil cache: %+v", got)
	}
}

// An org-only answer (an Anthropic API key without the directory) gives the
// org; the email comes from further down the order.
func TestResolveOrgOnly(t *testing.T) {
	s := fake(t, "/v1/organizations/me", "x-api-key", apiKey, 0, `{"id":"org-9","type":"organization","name":"Key Org"}`)
	c := New(Config{Lookup: true, Origin: s.URL, Client: s.Client()})
	c.Begin("kh-k", "", "api.anthropic.com", map[string]string{"x-api-key": apiKey}).Wait(2 * time.Second)
	got := c.Resolve(Call{KeyHash: "kh-k", OrgID: "org-header"})
	if got.OrgID != "org-9" || got.Source != SourceNone || got.Verified != "" {
		t.Errorf("%+v", got)
	}
}

// An observed account response links the call's credential to the account,
// recorded once per new link.
func TestObserve(t *testing.T) {
	var got []Learned
	c := New(Config{Learned: func(l Learned) { got = append(got, l) }})
	e := &events.Event{Host: "api.anthropic.com", Path: "/api/oauth/profile", Status: 200, KeyHash: "kh-1", KeyHint: "sk-ant…0001",
		Auth: &events.AuthIdentity{AccountID: "acc-1", Email: "a@example.com", Name: "Ada", OrgID: "org-1"}}
	c.Observe(e)
	c.Observe(e)
	if k, ok := c.Get("kh-1"); !ok || k.How != "observed" || k.Email != "a@example.com" {
		t.Errorf("cached %+v %v", k, ok)
	}
	if len(got) != 1 || got[0].How != "observed" || got[0].AccountID != "acc-1" {
		t.Errorf("learned %+v; want one", got)
	}
	for _, other := range []events.Event{
		{Host: "api.anthropic.com", Path: "/v1/messages", Status: 200, KeyHash: "kh-2", Auth: e.Auth},
		{Host: "api.anthropic.com", Path: "/api/oauth/profile", Status: 401, KeyHash: "kh-3", Auth: e.Auth},
		{Host: "chatgpt.com", Path: "/api/oauth/profile", Status: 200, KeyHash: "kh-4", Auth: e.Auth},
	} {
		c.Observe(&other)
		if _, ok := c.Get(other.KeyHash); ok {
			t.Errorf("learned from %s %s %d", other.Host, other.Path, other.Status)
		}
	}
}

func TestLRUBound(t *testing.T) {
	c := New(Config{MaxEntries: 2})
	for _, k := range []string{"a", "b", "c"} {
		c.Observe(&events.Event{Host: "api.anthropic.com", Path: "/api/oauth/profile", Status: 200, KeyHash: k,
			Auth: &events.AuthIdentity{AccountID: "acc-" + k, Email: k + "@example.com"}})
	}
	if _, ok := c.Get("a"); ok {
		t.Error("the least recently used entry stayed")
	}
	if s := c.Stats(); s.Entries != 2 || s.Accounts != 2 {
		t.Errorf("entries %d, accounts %d", s.Entries, s.Accounts)
	}
}

// The token never appears in logs, errors or records, whatever the lookup's
// outcome.
func TestTokenNeverLeaks(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)

	var learned bytes.Buffer
	for _, status := range []int{200, 401, 403, 500} {
		p := newProvider(t)
		p.status.Store(int32(status))
		c := newCache(p, nil, func(l Learned) { _ = json.NewEncoder(&learned).Encode(l) })
		c.Begin("kh", "sk-ant…0001", "api.anthropic.com", oauthHdrs).Wait(2 * time.Second)
		_ = json.NewEncoder(&learned).Encode(c.Stats())
		if _, err := (anthropicOAuth{}).Lookup(t.Context(), p.Client(), p.URL, oauthTok); err != nil {
			learned.WriteString(err.Error())
		}
	}
	// A transport failure's error, too.
	if _, err := (anthropicOAuth{}).Lookup(t.Context(), http.DefaultClient, "http://127.0.0.1:1", oauthTok); err != nil {
		learned.WriteString(err.Error())
	}
	for name, out := range map[string]string{"logs": logs.String(), "records and errors": learned.String()} {
		if strings.Contains(out, oauthTok) || strings.Contains(out, "FAKEoauth") {
			t.Errorf("the token is in the %s: %s", name, out)
		}
	}
	if !strings.Contains(logs.String(), "unauthorized") {
		t.Errorf("the failures weren't logged: %s", logs.String())
	}
}
