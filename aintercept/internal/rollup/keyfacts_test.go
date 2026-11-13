package rollup

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// FactsOf, from the state's rows, equals what the merge computes from the
// key's tables (DayOf, rawFirst, rawNewest, retainsContent).
func TestFactsOfEqualsTheMerge(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	base := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC).UnixNano()
	for trial := 0; trial < 20; trial++ {
		var rs []records.Record
		for i := 0; i < 1+rng.Intn(30); i++ {
			ts := base + int64(rng.Intn(10))*int64(time.Hour)
			id := fmt.Sprintf("r%d-%d", trial, i)
			var r records.Record
			switch rng.Intn(3) {
			case 0:
				c := &canon.Request{CallID: id, CorrelationID: id, SessionID: "s", StartNS: ts - 5, EndNS: ts, Status: 200,
					Host: "api.anthropic.com", Synthetic: rng.Intn(4) == 0, Product: canon.ProductClaudeCode}
				if rng.Intn(3) == 0 {
					c.DetailJSON = "{}"
				}
				r = records.Record{Kind: records.KindCallEnd, CallID: id, Call: c}
			case 1:
				c := &canon.Request{CallID: id, CorrelationID: id, SessionID: "s", StartNS: ts, Host: "api.anthropic.com"}
				r = records.Record{Kind: records.KindCallStart, CallID: id, Call: c}
			default:
				r = records.Record{Kind: records.KindClientEvent, EventKey: id, Event: &canon.Event{EventKey: id, SessionID: "s",
					RowNS: ts, NS: ts, Name: "tengu_x", Source: canon.ProductClaudeCode}}
			}
			r.Version, r.RecordID = records.Version, id
			r.RoutingKey = records.RoutingKey(&r)
			rs = append(rs, r)
		}
		key := rs[0].RoutingKey
		st := state.New(key)
		for _, r := range rs {
			if r.RoutingKey == key {
				st.Apply(r)
			}
		}
		tables := map[string][]stream.Row{}
		for t, byID := range st.Rows() {
			for id, v := range byID {
				tables[t] = append(tables[t], stream.Row{Key: key, ID: id, Value: v})
			}
		}
		got := FactsOf(key, st.Rows(), 7)
		first, earliest := rawFirst(tables)
		want := KeyFacts{Day: DayOf(key, tables, 7), FirstRequestNS: first, EarliestNS: earliest, RawNewestNS: rawNewest(tables),
			ContentRetained: retainsContent(tables)}
		if got != want {
			t.Fatalf("trial %d: %+v, want %+v", trial, got, want)
		}
	}
	for _, key := range []string{"a:acct", "c:h:2026-11-12", "ip:1.2.3.4:2026-11-12T05"} {
		if got, want := FactsOf(key, nil, 7).Day, DayOf(key, nil, 7); got != want {
			t.Errorf("%s: day %q, want %q", key, got, want)
		}
	}
}
