package identity

import (
	"container/list"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Config configures a Cache. Zero values take the defaults.
type Config struct {
	// Lookup asks the provider about each new credential. Off: the cache
	// still learns from observed account responses.
	Lookup bool
	// Providers limits lookups to these resolvers' providers (empty: all).
	Providers []string
	// Timeout bounds one lookup (2 s).
	Timeout time.Duration
	// MaxInFlight bounds concurrent lookups (8); RatePerSecond and Burst
	// bound how fast they start (5/s, burst 20). A credential whose lookup
	// can't start is a miss; its next call tries again.
	MaxInFlight   int
	RatePerSecond float64
	Burst         int
	// MaxEntries bounds each of the two maps (by credential, by account),
	// least recently used out first (100 000).
	MaxEntries int
	// TTL keeps an answer (24 h); NegativeTTL a refusal (401, 403), an
	// answer that names no one, or a response of the wrong shape (1 h);
	// ErrorTTL a failure (timeout, 5xx) or an incomplete answer (60 s).
	TTL, NegativeTTL, ErrorTTL time.Duration
	// Origin, for tests only, sends every lookup to this origin instead of
	// the provider's.
	Origin string
	// Client makes the lookups, straight to the provider (default: a client
	// with the system roots and no proxy through Envoy).
	Client *http.Client
	// Resolvers are the providers' (default: Resolvers(nil)).
	Resolvers []Resolver
	// Learned receives each credential's owner as it's learned, to record.
	// It must not block.
	Learned func(Learned)
	Now     func() time.Time
}

// Learned is a credential's owner, just learned.
type Learned struct {
	KeyHash, KeyHint string
	Owner
	// How: "lookup" (asked the provider) or "observed" (its account
	// response to a call with the credential).
	How string
	NS  int64
}

// Known is what the cache holds for a credential.
type Known struct {
	Owner
	How string
	NS  int64 // when it was learned
}

// Pending is a credential's lookup in flight.
type Pending struct{ done chan struct{} }

// Done is closed when the lookup has ended.
func (p *Pending) Done() <-chan struct{} { return p.done }

// Wait waits up to d for the lookup to end, and reports whether it has. A nil
// Pending has nothing to wait for.
func (p *Pending) Wait(d time.Duration) bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
	}
	if d <= 0 {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// Cache is one pod's credential owners (see the package comment). Safe for
// concurrent use; a nil Cache knows nothing and looks nothing up.
type Cache struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	creds    *lru // key hash → *Known (nil: refused or failed)
	accounts *lru // account id → *Known
	inflight map[string]*Pending
	closed   bool
	lookups  sync.WaitGroup
	tokens   float64
	refilled time.Time
	stats    counters
}

// New builds a Cache.
func New(cfg Config) *Cache {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 8
	}
	if cfg.RatePerSecond <= 0 {
		cfg.RatePerSecond = 5
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 100_000
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.NegativeTTL <= 0 {
		cfg.NegativeTTL = time.Hour
	}
	if cfg.ErrorTTL <= 0 {
		cfg.ErrorTTL = time.Minute
	}
	if cfg.Resolvers == nil {
		cfg.Resolvers = Resolvers(nil)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Learned == nil {
		cfg.Learned = func(Learned) {}
	}
	c := &Cache{cfg: cfg, client: cfg.Client, creds: newLRU(cfg.MaxEntries), accounts: newLRU(cfg.MaxEntries),
		inflight: map[string]*Pending{}, tokens: float64(cfg.Burst), refilled: cfg.Now()}
	if c.client == nil {
		// Straight to the provider: never back through Envoy (no proxy).
		c.client = &http.Client{Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true,
			MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 5 * time.Second}}
	}
	c.stats.init()
	return c
}

// Get is the credential's confirmed owner, when known.
func (c *Cache) Get(keyHash string) (Known, bool) {
	if c == nil || keyHash == "" {
		return Known{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.creds.get(keyHash, c.cfg.Now()); ok && k != nil {
		return *k, true
	}
	return Known{}, false
}

// ByAccount is the owner last confirmed for an account, when known.
func (c *Cache) ByAccount(accountID string) (Known, bool) {
	if c == nil || accountID == "" {
		return Known{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.accounts.get(accountID, c.cfg.Now()); ok && k != nil {
		return *k, true
	}
	return Known{}, false
}

// Begin is called at a request's headers (raw, before redaction): it starts
// a lookup for a credential the cache has no answer for, when a resolver can
// look it up. It returns the credential's lookup in flight (started now or
// by an earlier call: one per credential), or nil when there is none.
func (c *Cache) Begin(keyHash, keyHint, host string, hdrs map[string]string) *Pending {
	if c == nil || keyHash == "" {
		return nil
	}
	now := c.cfg.Now()
	c.mu.Lock()
	if _, ok := c.creds.get(keyHash, now); ok {
		c.mu.Unlock()
		return nil
	}
	if p := c.inflight[keyHash]; p != nil {
		c.mu.Unlock()
		return p
	}
	if !c.cfg.Lookup || c.closed {
		c.mu.Unlock()
		return nil
	}
	r, token := c.resolver(host, hdrs)
	if r == nil {
		c.mu.Unlock()
		return nil
	}
	if len(c.inflight) >= c.cfg.MaxInFlight || !c.take(now) {
		c.stats.lookups["ratelimited"]++
		c.mu.Unlock()
		return nil
	}
	p := &Pending{done: make(chan struct{})}
	c.inflight[keyHash] = p
	c.lookups.Add(1)
	c.mu.Unlock()
	go c.lookup(r, token, keyHash, keyHint, p)
	return p
}

// Close starts no more lookups and waits for those in flight (each bounded
// by Timeout), so what they learn is passed to Learned before Close returns.
func (c *Cache) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.lookups.Wait()
}

// resolver is the first allowed resolver that takes the request's
// credential, and the token.
func (c *Cache) resolver(host string, hdrs map[string]string) (Resolver, string) {
	for _, r := range c.cfg.Resolvers {
		if len(c.cfg.Providers) > 0 && !slices.Contains(c.cfg.Providers, r.Provider()) {
			continue
		}
		if tok, ok := r.Credential(host, hdrs); ok && tok != "" {
			return r, tok
		}
	}
	return nil, ""
}

// take takes a token from the rate limit's bucket. Called with mu held.
func (c *Cache) take(now time.Time) bool {
	c.tokens = min(float64(c.cfg.Burst), c.tokens+now.Sub(c.refilled).Seconds()*c.cfg.RatePerSecond)
	c.refilled = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

// lookup asks the provider, keeps the answer and records it. The token lives
// only here: it's never logged, kept or put in an error.
func (c *Cache) lookup(r Resolver, token, keyHash, keyHint string, p *Pending) {
	defer c.lookups.Done()
	origin := r.Origin()
	if c.cfg.Origin != "" {
		origin = c.cfg.Origin
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	t := time.Now()
	o, err := r.Lookup(ctx, c.client, origin, token)
	took := time.Since(t)
	cancel()
	res := result(o, err)
	now := c.cfg.Now()
	var learned *Learned
	c.mu.Lock()
	c.stats.lookups[res]++
	c.stats.lookupMS.add(took)
	switch {
	case res == "ok":
		ttl := c.cfg.TTL
		if o.Incomplete {
			ttl = c.cfg.ErrorTTL
		}
		k := &Known{Owner: *o, How: "lookup", NS: now.UnixNano()}
		k.Incomplete = false
		c.store(keyHash, k, now.Add(ttl))
		learned = &Learned{KeyHash: keyHash, KeyHint: keyHint, Owner: k.Owner, How: "lookup", NS: k.NS}
	case res == "timeout" || res == "error":
		c.creds.put(keyHash, nil, now.Add(c.cfg.ErrorTTL))
	default:
		c.creds.put(keyHash, nil, now.Add(c.cfg.NegativeTTL))
	}
	delete(c.inflight, keyHash)
	c.mu.Unlock()
	if res != "ok" {
		slog.Info("identity lookup", "provider", r.Provider(), "key_hint", keyHint, "result", res, "ms", took.Milliseconds())
	}
	if learned != nil {
		c.cfg.Learned(*learned)
	}
	close(p.done)
}

// store keeps a credential's owner, and its account's. Called with mu held.
func (c *Cache) store(keyHash string, k *Known, expires time.Time) {
	c.creds.put(keyHash, k, expires)
	if k.AccountID != "" && k.Person() {
		c.accounts.put(k.AccountID, k, expires)
	}
}

// Observe learns from a call's complete row (after extraction): a provider's
// account response to a call with the credential names its owner. A
// credential newly linked to an account is recorded; one already known with
// the same account is only refreshed.
func (c *Cache) Observe(e *events.Event) {
	if c == nil || e.KeyHash == "" || e.Synthetic {
		return
	}
	for _, r := range c.cfg.Resolvers {
		ob, ok := r.(Observer)
		if !ok {
			continue
		}
		o := ob.Observe(e)
		if o == nil || o.AccountID == "" {
			continue
		}
		now := c.cfg.Now()
		k := &Known{Owner: *o, How: "observed", NS: now.UnixNano()}
		c.mu.Lock()
		old, had := c.creds.get(e.KeyHash, now)
		c.store(e.KeyHash, k, now.Add(c.cfg.TTL))
		c.mu.Unlock()
		if !had || old == nil || old.AccountID != o.AccountID {
			c.cfg.Learned(Learned{KeyHash: e.KeyHash, KeyHint: e.KeyHint, Owner: k.Owner, How: "observed", NS: k.NS})
		}
		return
	}
}

// ── LRU ──────────────────────────────────────────────────────────────────────

type lru struct {
	max int
	ll  *list.List
	m   map[string]*list.Element
}

type lruEntry struct {
	key     string
	val     *Known
	expires time.Time
}

func newLRU(max int) *lru { return &lru{max: max, ll: list.New(), m: map[string]*list.Element{}} }

// get is a live entry's value (nil for a negative one), touching it; an
// expired one is removed.
func (l *lru) get(key string, now time.Time) (*Known, bool) {
	el := l.m[key]
	if el == nil {
		return nil, false
	}
	en := el.Value.(*lruEntry)
	if !now.Before(en.expires) {
		l.ll.Remove(el)
		delete(l.m, key)
		return nil, false
	}
	l.ll.MoveToFront(el)
	return en.val, true
}

func (l *lru) put(key string, val *Known, expires time.Time) {
	if el := l.m[key]; el != nil {
		en := el.Value.(*lruEntry)
		en.val, en.expires = val, expires
		l.ll.MoveToFront(el)
		return
	}
	l.m[key] = l.ll.PushFront(&lruEntry{key: key, val: val, expires: expires})
	for l.ll.Len() > l.max {
		old := l.ll.Back()
		l.ll.Remove(old)
		delete(l.m, old.Value.(*lruEntry).key)
	}
}

func (l *lru) len() int { return l.ll.Len() }
