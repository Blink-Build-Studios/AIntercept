package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// checkPresence requires the stream's presence rows to equal the reference's
// over the same records.
func checkPresence(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	want, err := refPresence(reference(rs))
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	got := o.presenceRows()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: presence rows differ\n got %+v\nwant %+v", label, got, want)
	}
}

// atomicCase is one record applied to a known state: before is applied first,
// then rec; the rows after must equal the reference, and the record must take
// the named cases.
type atomicCase struct {
	name   string
	before func() []records.Record
	rec    func() records.Record
	cases  []string
}

func hidden(c *canon.Request) *canon.Request { c.HiddenKind = "prewarm"; return c }

var presenceCases = []atomicCase{
	{"first event opens the span", nil,
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{}`) },
		[]string{CaseEventSpan, CasePresenceRecut}},
	{"later event extends the last focused segment",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return ccEvent("s1", "tengu_api_query", 5000, `{}`) },
		[]string{CaseEventSpan, CasePresenceRecut}},
	{"earlier event moves the start",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 5000, `{}`)} },
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{}`) },
		[]string{CaseEventSpan, CasePresenceRecut}},
	{"event inside the span changes nothing",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`)}
		},
		func() records.Record { return ccEvent("s1", "tengu_api_query", 5000, `{}`) },
		[]string{CaseEventSpan}},
	{"blur window cuts an away segment",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`)}
		},
		func() records.Record { return blur("s1", 6000, 2000) },
		[]string{CaseEventSpan, CaseEventBlur, CasePresenceRecut}},
	{"blur window joining one it touches",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`), blur("s1", 4000, 1000)}
		},
		func() records.Record { return blur("s1", 6000, 2000) },
		[]string{CaseEventSpan, CaseEventBlur, CasePresenceRecut}},
	{"blur window bridging two",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`),
				blur("s1", 3000, 1000), blur("s1", 7000, 1000)}
		},
		func() records.Record { return blur("s1", 6500, 4000) },
		[]string{CaseEventSpan, CaseEventBlur, CasePresenceRecut}},
	{"blur window inside one already there",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`), blur("s1", 7000, 4000)}
		},
		func() records.Record { return blur("s1", 6000, 1000) },
		[]string{CaseEventSpan, CaseEventBlur}},
	{"blur window reaching before the span is clipped",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 5000, `{}`)} },
		func() records.Record { return blur("s1", 6000, 4000) },
		[]string{CaseEventSpan, CaseEventBlur, CasePresenceRecut}},
	{"blur of zero, negative or a string",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record {
			return ccEvent("s1", "tengu_return_to_session", 3000, `{"blurDurationMs":"500"}`)
		},
		[]string{CaseEventSpan, CaseEventBlur, CasePresenceRecut}},
	{"blur on an event presence doesn't read", nil,
		func() records.Record { return ccEvent("s1", "tengu_other", 3000, `{"blurDurationMs":500}`) },
		[]string{CaseEventSpan, CasePresenceRecut}},
	{"sleep window is a row",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return sleep("s1", 9000, 3000) },
		[]string{CaseEventSpan, CaseEventSleep, CasePresenceSleepRow, CasePresenceRecut}},
	{"sleep window joining one",
		func() []records.Record { return []records.Record{sleep("s1", 9000, 3000)} },
		func() records.Record { return sleep("s1", 7000, 3000) }, // the event also moves the span's start
		[]string{CaseEventSpan, CaseEventSleep, CasePresenceSleepRow, CasePresenceRecut}},
	{"sleep window inside one",
		func() []records.Record { return []records.Record{sleep("s1", 9000, 5000)} },
		func() records.Record { return sleep("s1", 8000, 1000) },
		[]string{CaseEventSpan, CaseEventSleep, CasePresenceRecut}},
	{"visible call extends the span",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return callEnd(ccCall("c1", "s1", 4000)) },
		[]string{CaseCallVisible, CasePresenceRecut}},
	{"hidden call doesn't count",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 4000))) },
		[]string{CaseCallNotVisible}},
	{"call in flight counts",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return callStart(ccCall("c1", "s1", 4000)) },
		[]string{CaseStartVisible, CasePresenceRecut}},
	{"hidden call's ending takes back its start",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), callStart(ccCall("c1", "s1", 4000))}
		},
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 4000))) },
		[]string{CaseCallNotVisible, CasePresenceRecut}},
	{"start after its ending",
		func() []records.Record { return []records.Record{callEnd(ccCall("c1", "s1", 4000))} },
		func() records.Record { return callStart(ccCall("c1", "s1", 4000)) },
		[]string{CaseStartAfterEnd}},
	{"count_tokens in flight doesn't count",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record {
			c := ccCall("c1", "s1", 4000)
			c.Path = "/v1/messages/count_tokens"
			return callStart(c)
		},
		[]string{CaseStartNotVisible}},
	{"a call with no start time counts at its end",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { c := ccCall("c1", "s1", 4000); c.StartNS = 0; return callEnd(c) },
		[]string{CaseCallVisible, CasePresenceRecut}},
	{"a synthetic call doesn't count", nil,
		func() records.Record { c := ccCall("c1", "s1", 4000); c.Synthetic = true; return callEnd(c) },
		[]string{CaseCallNotVisible}},
	{"a Codex turn in flight doesn't count", nil,
		func() records.Record { return callStart(codexCall("x1#1", "s1", "t", 4000)) },
		[]string{CaseStartNotVisible}},
	{"a Codex call is visible in its thread", nil,
		func() records.Record { return callEnd(codexCall("x1", "s1", "t", 4000)) },
		[]string{CaseCallVisible, CasePresenceRecut}},
	{"another product's call isn't", nil,
		func() records.Record { c := ccCall("c1", "s1", 4000); c.Product = "other"; return callEnd(c) },
		[]string{CaseCallNotVisible}},
	{"an event of no session", nil,
		func() records.Record { return ccEvent("", "tengu_api_query", 1000, `{}`) },
		[]string{CaseEventNotSession}},
	{"an account sighting", nil,
		func() records.Record {
			return rec(records.Record{Kind: records.KindAccount, Account: &records.Account{AccountID: "a1", Email: "x@y", NS: 1}})
		},
		[]string{CaseOtherRecord}},
}

func TestPresenceAtomic(t *testing.T) {
	for _, c := range presenceCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{} // a case's events are its batches' whole
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkPresence(t, c.name, append(before, r), o)
			if got := only(d.Cases, presenceCaseSet); !reflect.DeepEqual(got, c.cases) {
				t.Fatalf("cases %v, want %v", got, c.cases)
			}
			if d2 := o.apply(r); !reflect.DeepEqual(d2.Cases, []string{CaseDuplicate}) || len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

// presenceCaseSet is the cases the presence tests name: the session's inputs
// and presence's own.
var presenceCaseSet = map[string]bool{
	CaseOtherRecord: true, CaseEventNotSession: true, CaseEventSpan: true, CaseEventBlur: true, CaseEventSleep: true,
	CaseCallVisible: true, CaseCallNotVisible: true, CaseStartVisible: true, CaseStartNotVisible: true,
	CaseStartAfterEnd: true, CasePresenceRecut: true, CasePresenceSleepRow: true,
}

// only is the cases in set, in order.
func only(cases []string, set map[string]bool) []string {
	var out []string
	for _, c := range cases {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// Every case has an atomic test.
func TestEveryCaseHasAnAtomicTest(t *testing.T) {
	covered := map[string]bool{CaseDuplicate: true} // every atomic test applies its record twice
	for _, cs := range [][]atomicCase{presenceCases, eventCases, dimCases, promptCases, requestCases, sessionCases} {
		for _, c := range cs {
			for _, x := range c.cases {
				covered[x] = true
			}
		}
	}
	for _, c := range AllCases {
		if !covered[c] {
			t.Errorf("case %q has no atomic test", c)
		}
	}
}

// Short sequences: every order of a few records ends in the reference's rows.
func TestPresenceShortSequencesEveryOrder(t *testing.T) {
	sets := map[string]func() []records.Record{
		"span and a blur": func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`), blur("s1", 6000, 2000)}
		},
		"touching blurs and a sleep": func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), blur("s1", 4000, 1000), blur("s1", 6000, 2000),
				sleep("s1", 9000, 1000), ccEvent("s1", "tengu_api_query", 12000, `{}`)}
		},
		"call in flight, then hidden": func() []records.Record {
			c := ccCall("c1", "s1", 15000)
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), callStart(c), callEnd(hidden(ccCall("c1", "s1", 15000))), blur("s1", 3000, 1500)}
		},
	}
	for name, mk := range sets {
		batchN = map[string]int64{}
		rs := mk()
		permute(len(rs), func(p []int) {
			ordered := make([]records.Record, len(rs))
			for i, j := range p {
				ordered[i] = rs[j]
			}
			checkPresence(t, fmt.Sprintf("%s %v", name, p), rs, stream(ordered))
		})
	}
}

func permute(n int, fn func([]int)) {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			fn(p)
			return
		}
		for i := k; i < n; i++ {
			p[k], p[i] = p[i], p[k]
			rec(k + 1)
			p[k], p[i] = p[i], p[k]
		}
	}
	rec(0)
}

// Long generated sequences: many sessions' events and calls, shuffled and with
// duplicates, applied one at a time (and through a checkpoint), end in the
// reference's rows.
func TestPresenceGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 20/seedFrac; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateSessions(rng, 4, 300)
		in := shuffled(rng, rs, 40)
		o := stream(in[:len(in)/2])
		for k, s := range o.states { // a checkpoint halfway: the rest applies to decoded states
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
		checkPresence(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

// generateSessions is n sessions of about per records each: events of every
// presence kind (blur and sleep windows of every size, values as strings, zero
// and negative), events that aren't presence's, visible, hidden and in-flight
// calls, and calls the session doesn't count.
func generateSessions(rng *rand.Rand, n, per int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("sess-%d", i)
		t := int64(1_000_000 + rng.Intn(100_000))
		for j := 0; j < per; j++ {
			t += int64(rng.Intn(5000))
			switch k := rng.Intn(10); {
			case k < 3:
				out = append(out, ccEvent(s, "tengu_api_query", t, `{}`))
			case k < 5:
				d := []string{`%d`, `"%d"`, `0`, `-5`}[rng.Intn(4)]
				if d != `0` && d != `-5` {
					d = fmt.Sprintf(d, rng.Intn(20000))
				}
				out = append(out, ccEvent(s, "tengu_return_to_session", t, `{"blurDurationMs":`+d+`}`))
			case k < 6:
				out = append(out, ccEvent(s, "tengu_event_loop_stall", t,
					fmt.Sprintf(`{"likely_sleep":%v,"clock_jump_ms":%d}`, rng.Intn(3) > 0, rng.Intn(30000))))
			case k < 7:
				out = append(out, ccEvent(s, "tengu_other", t, `{"blurDurationMs":100}`))
			default:
				c := ccCall(fmt.Sprintf("gc-%d-%d", i, j), s, t)
				if rng.Intn(5) == 0 {
					c.HiddenKind = "prewarm"
				}
				if rng.Intn(8) == 0 {
					c.Path = "/v1/messages/count_tokens"
				}
				if rng.Intn(4) > 0 {
					out = append(out, callStart(c))
				}
				if rng.Intn(6) > 0 {
					out = append(out, callEnd(c))
				}
			}
		}
	}
	return out
}

// Flat cost: one more record costs the same work whether the session holds 10,
// 1,000 or 100,000.
func TestPresenceFlatCost(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100,000-record session")
	}
	type cost struct {
		allocs float64
		perOp  time.Duration
	}
	measure := func(n int) cost {
		s := New("s:big")
		for i := 0; i < n; i++ {
			var r records.Record
			if i%4 == 0 {
				r = blur("big", int64(i)*1000+500, 300)
			} else {
				r = ccEvent("big", "tengu_api_query", int64(i)*1000, `{}`)
			}
			s.Apply(r)
		}
		mid, end := int64(n)*500, int64(n)*1000
		i := 0
		next := func() records.Record {
			i++
			switch i % 3 {
			case 0:
				return blur("big", mid+int64(i)*7, 200) // a window in the middle
			case 1:
				return ccEvent("big", "tengu_api_query", end+int64(i), `{}`) // the span grows
			default:
				return sleep("big", mid+int64(i)*11, 100)
			}
		}
		pre := make([]records.Record, 3000)
		for k := range pre {
			pre[k] = next()
		}
		k := 0
		allocs := testing.AllocsPerRun(1000, func() { s.Apply(pre[k]); k++ })
		start := time.Now()
		for ; k < len(pre); k++ {
			s.Apply(pre[k])
		}
		return cost{allocs, time.Since(start) / time.Duration(len(pre)-1000)}
	}
	small, big := measure(10), measure(flatBig)
	mid := measure(1_000)
	t.Logf("per record: n=10 %.1f allocs %v; n=1,000 %.1f allocs %v; n=100,000 %.1f allocs %v",
		small.allocs, small.perOp, mid.allocs, mid.perOp, big.allocs, big.perOp)
	if big.allocs > small.allocs*1.5+4 {
		t.Errorf("allocations grow with the session: %.1f at 10, %.1f at 100,000", small.allocs, big.allocs)
	}
	if big.perOp > 8*small.perOp+20*time.Microsecond {
		t.Errorf("time grows with the session: %v at 10, %v at 100,000", small.perOp, big.perOp)
	}
}

// A checkpoint round trip keeps every row, and a damaged one is an error.
func TestEncodeDecode(t *testing.T) {
	o := stream([]records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), blur("s1", 3000, 500), sleep("s1", 4000, 100)})
	s := o.states["s:s1"]
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s2.Rows(), s.Rows()) {
		t.Fatalf("rows differ after a round trip")
	}
	if _, err := Decode([]byte("not a checkpoint")); err == nil {
		t.Fatal("a damaged checkpoint decoded")
	}
}
