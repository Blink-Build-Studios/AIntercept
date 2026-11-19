package identity

import (
	"strconv"
	"time"
)

// counters are the cache's, for the observer's /stats. Guarded by Cache.mu.
type counters struct {
	lookups  map[string]uint64 // by result
	lookupMS hist
	waitMS   map[string]*hist // by mode: observe, enforce
	hits     map[string]uint64
	misses   uint64
	mismatch uint64
}

func (s *counters) init() {
	s.lookups = map[string]uint64{}
	s.waitMS = map[string]*hist{"observe": {}, "enforce": {}}
	s.hits = map[string]uint64{}
}

// hist counts durations in fixed buckets (ms upper bounds; the last is
// everything above).
type hist [len(histBounds) + 1]uint64

var histBounds = [...]int64{10, 50, 100, 300, 1000, 2000}

func (h *hist) add(d time.Duration) {
	ms := d.Milliseconds()
	for i, b := range histBounds {
		if ms <= b {
			h[i]++
			return
		}
	}
	h[len(histBounds)]++
}

func (h *hist) out() map[string]uint64 {
	m := map[string]uint64{}
	for i, n := range h {
		if n == 0 {
			continue
		}
		k := "inf"
		if i < len(histBounds) {
			k = "le" + strconv.FormatInt(histBounds[i], 10)
		}
		m[k] = n
	}
	return m
}

// Note counts a call's resolved identity: a hit by its source, or a miss,
// and an account mismatch. Once per call.
func (c *Cache) Note(id Identity) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if id.Source == SourceNone {
		c.stats.misses++
	} else {
		c.stats.hits[id.Source.String()]++
	}
	if id.Mismatch {
		c.stats.mismatch++
	}
}

// NoteWait counts how long a call waited for its credential's lookup, in
// mode "observe" or "enforce".
func (c *Cache) NoteWait(mode string, d time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.stats.waitMS[mode]; h != nil {
		h.add(d)
	}
}

// Stats are the cache's counters.
type Stats struct {
	Lookup          bool                         `json:"lookup"`
	Entries         int                          `json:"entries"`
	Accounts        int                          `json:"accounts"`
	InFlight        int                          `json:"in_flight"`
	Hits            map[string]uint64            `json:"hits"`
	Misses          uint64                       `json:"misses"`
	Lookups         map[string]uint64            `json:"lookups"`
	LookupMS        map[string]uint64            `json:"lookup_ms"`
	WaitMS          map[string]map[string]uint64 `json:"wait_ms"`
	AccountMismatch uint64                       `json:"account_mismatch"`
}

// Stats returns the counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Lookup: c.cfg.Lookup, Entries: c.creds.len(), Accounts: c.accounts.len(), InFlight: len(c.inflight),
		Hits: map[string]uint64{}, Misses: c.stats.misses, Lookups: map[string]uint64{}, LookupMS: c.stats.lookupMS.out(),
		WaitMS: map[string]map[string]uint64{}, AccountMismatch: c.stats.mismatch}
	for k, v := range c.stats.hits {
		s.Hits[k] = v
	}
	for k, v := range c.stats.lookups {
		s.Lookups[k] = v
	}
	for k, h := range c.stats.waitMS {
		s.WaitMS[k] = h.out()
	}
	return s
}
