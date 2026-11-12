package stream

import (
	"fmt"
	"math/rand"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// gen makes synthetic records: calls and events across a set of sessions and
// credentials, with times that wander backwards as well as forwards, as real
// late data does.
type gen struct {
	rng      *rand.Rand
	sessions int
	prefix   string // session names' prefix (default "s")
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
		r = records.Record{Kind: records.KindClientEvent, Event: &canon.Event{SessionID: fmt.Sprintf("%s%d", g.sessionPrefix(), g.rng.Intn(g.sessions)), RowNS: g.ts, NS: g.ts}}
	default:
		r = records.Record{Kind: records.KindCallEnd, Call: &canon.Request{CallID: id, SessionID: fmt.Sprintf("%s%d", g.sessionPrefix(), g.rng.Intn(g.sessions)), EndNS: g.ts}}
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

func (g *gen) sessionPrefix() string {
	if g.prefix == "" {
		return "s"
	}
	return g.prefix
}
