package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The fact tables: the rows today's processor writes as requests, pending and
// analytics, which the drawer, /status and the directory read. Over every
// key, they're the reference's input: each non-synthetic call's ending once
// (under its own key, not its copy's), each call still in flight as the
// processor holds it, every event.

// factsOf is the stored fact rows, each table in its id order.
func factsOf(o *streamOut) (reqs []canon.Request, pend []canon.Pending, evs []canon.Event) {
	for _, r := range o.rows[TableFactRequests] {
		reqs = append(reqs, r.(canon.Request))
	}
	for _, r := range o.rows[TableFactPending] {
		pend = append(pend, r.(canon.Pending))
	}
	for _, r := range o.rows[TableFactEvents] {
		evs = append(evs, r.(canon.Event))
	}
	sortFacts(reqs, pend, evs)
	return reqs, pend, evs
}

func sortFacts(reqs []canon.Request, pend []canon.Pending, evs []canon.Event) {
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].CallID < reqs[j].CallID })
	sort.Slice(pend, func(i, j int) bool { return pend[i].CorrelationID < pend[j].CorrelationID })
	sort.Slice(evs, func(i, j int) bool { return evs[i].EventKey < evs[j].EventKey })
}

func checkFacts(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	in := refInput(rs)
	var reqs []canon.Request
	for _, c := range in.Requests {
		if !c.Synthetic {
			reqs = append(reqs, c)
		}
	}
	pend, evs := append([]canon.Pending(nil), in.Pending...), append([]canon.Event(nil), in.Events...)
	sortFacts(reqs, pend, evs)
	gr, gp, ge := factsOf(o)
	if !(len(gr) == 0 && len(reqs) == 0) && !reflect.DeepEqual(gr, reqs) {
		t.Fatalf("%s: requests differ\n got %+v\nwant %+v", label, gr, reqs)
	}
	if !(len(gp) == 0 && len(pend) == 0) && !reflect.DeepEqual(gp, pend) {
		t.Fatalf("%s: pending differs\n got %+v\nwant %+v", label, gp, pend)
	}
	if !(len(ge) == 0 && len(evs) == 0) && !reflect.DeepEqual(ge, evs) {
		t.Fatalf("%s: events differ\n got %+v\nwant %+v", label, ge, evs)
	}
}

// keyedCall is a call with no session: it routes by its credential and day.
func keyedCall(id string, t int64) *canon.Request {
	c := ccCall(id, "", t)
	c.KeyHash = "kf"
	return c
}

func startKeyOf(c *canon.Request) string {
	return records.RoutingKey(&records.Record{Kind: records.KindCallStart, Call: c})
}

var factCases = []atomicCase{
	{"an ended call", nil,
		func() records.Record { return callEnd(ccCall("c1", "s1", 1000)) },
		[]string{CaseFactRequest}},
	{"a synthetic call: no row, its key's probe times", nil,
		func() records.Record {
			c := ccCall("c1", "s1", 1000)
			c.Synthetic = true
			return callEnd(c)
		},
		[]string{CaseFactProbe}},
	{"a call in flight", nil,
		func() records.Record { return callStart(ccCall("c1", "s1", 1000)) },
		[]string{CaseFactPending}},
	{"a WebSocket turn in flight: not pending (its connection is)", nil,
		func() records.Record { return callStart(ccCall("w1#1", "s1", 1000)) },
		nil},
	{"its ending takes the call out of pending", func() []records.Record {
		return []records.Record{callStart(ccCall("c1", "s1", 1000))}
	},
		func() records.Record { return callEnd(ccCall("c1", "s1", 1000)) },
		[]string{CaseFactPendingGone, CaseFactRequest}},
	{"a start after its ending: not pending", func() []records.Record {
		return []records.Record{callEnd(ccCall("c1", "s1", 1000))}
	},
		func() records.Record { return callStart(ccCall("c1", "s1", 1000)) },
		nil},
	{"an ending's copy under its start's key ends the pending row, and writes no request (the ending did)", func() []records.Record {
		c := keyedCall("c1", 1000)
		c.SessionID = "s1"
		return []records.Record{callStart(keyedCall("c1", 1000)), callEnd(c)}
	},
		func() records.Record {
			c := keyedCall("c1", 1000)
			c.SessionID = "s1"
			return endCopy(c, startKeyOf(keyedCall("c1", 1000)))
		},
		[]string{CaseFactPendingGone}},
	{"an event", nil,
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{}`) },
		[]string{CaseFactEvent}},
}

var factCaseSet = map[string]bool{CaseFactProbe: true, CaseFactRequest: true, CaseFactPending: true, CaseFactPendingGone: true, CaseFactEvent: true}

func TestFactsAtomic(t *testing.T) {
	for _, c := range factCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkFacts(t, c.name, append(before, r), o)
			if got := only(d.Cases, factCaseSet); !reflect.DeepEqual(got, c.cases) {
				t.Fatalf("cases %v, want %v", got, c.cases)
			}
			if d2 := o.apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

func TestFactsShortSequencesEveryOrder(t *testing.T) {
	mk := func() []records.Record {
		moved := keyedCall("c2", 2000)
		ended := keyedCall("c2", 2000)
		ended.SessionID = "s1"
		syn := ccCall("c4", "s1", 4000)
		syn.Synthetic = true
		return []records.Record{
			callStart(ccCall("c1", "s1", 1000)), callEnd(ccCall("c1", "s1", 1000)),
			callStart(moved), callEnd(ended), endCopy(ended, startKeyOf(moved)),
			callStart(ccCall("c3", "s1", 3000)), callEnd(syn),
			ccEvent("s1", "tengu_api_query", 1500, `{}`),
		}
	}
	base := mk()
	permute(len(base), func(order []int) {
		batchN = map[string]int64{}
		rs := mk()
		in := make([]records.Record, len(order))
		for i, k := range order {
			in[i] = rs[k]
		}
		checkFacts(t, fmt.Sprint(order), rs, stream(in))
	})
}

// generateFacts is calls started and ended under one key or two (with the
// ending's copy), some synthetic, some still in flight, WebSocket turns, events.
func generateFacts(rng *rand.Rand, n int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		t := int64(1_000_000 + i*1000)
		id := fmt.Sprintf("g-%d", i)
		switch k := rng.Intn(6); {
		case k < 2:
			c := ccCall(id, fmt.Sprintf("s%d", rng.Intn(3)), t)
			c.Synthetic = rng.Intn(5) == 0
			if rng.Intn(3) > 0 {
				out = append(out, callStart(c))
			}
			if rng.Intn(4) > 0 {
				out = append(out, callEnd(c))
			}
		case k < 4:
			start := keyedCall(id, t)
			end := keyedCall(id, t)
			end.SessionID = fmt.Sprintf("s%d", rng.Intn(3))
			out = append(out, callStart(start))
			if rng.Intn(4) > 0 {
				out = append(out, callEnd(end), endCopy(end, startKeyOf(start)))
			}
		case k < 5:
			out = append(out, callStart(ccCall(id+"#1", "s1", t)), callEnd(ccCall(id+"#1", "s1", t)))
		default:
			out = append(out, ccEvent(fmt.Sprintf("s%d", rng.Intn(3)), "tengu_api_query", t, `{}`))
		}
	}
	return out
}

func TestFactsGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 20/seedFrac; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := append(generateFacts(rng, 120), generateRequests(rng, 2, 60)...)
		in := shuffled(rng, rs, 40)
		o := stream(in[:len(in)/2])
		for k, s := range o.states {
			b, err := Encode(s)
			if err != nil {
				t.Fatal(err)
			}
			if o.states[k], err = Decode(b); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range in[len(in)/2:] {
			o.apply(r)
		}
		checkFacts(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

// A key's probe calls (synthetic: never shown) are one row, their first time
// (a call's start, else its end) and newest (its end, else its start): what
// /status counts of them, as today's store reads its probes.
func TestFactProbesFoldAKeysProbes(t *testing.T) {
	probe := func(id string, start, end int64) records.Record {
		c := &canon.Request{CallID: id, CorrelationID: id, StartNS: start, EndNS: end, Host: "h", Method: "GET", Path: "/", Synthetic: true}
		return callEnd(c)
	}
	o := stream([]records.Record{probe("p2", 5000, 5002), probe("p1", 1000, 1002), probe("p3", 0, 3000)})
	got := o.rows[TableFactProbes]
	if len(got) != 1 {
		t.Fatalf("probe rows %v, want one", got)
	}
	for _, r := range got {
		if p := r.(Probes); p != (Probes{FirstNS: 1000, NewestNS: 5002}) {
			t.Fatalf("probes %+v, want first 1000, newest 5002", p)
		}
	}
	if d := o.apply(probe("p1", 1000, 1002)); len(d.Changes) != 0 {
		t.Fatalf("an old probe changed something: %+v", d)
	}
}
