package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

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

// spanCases are the session's own inputs: its span, and records no session
// table reads.
var spanCases = []atomicCase{
	{"first event opens the span", nil,
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{}`) },
		[]string{CaseEventSpan}},
	{"later event extends the span",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return ccEvent("s1", "tengu_api_query", 5000, `{}`) },
		[]string{CaseEventSpan}},
	{"earlier event moves the start",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 5000, `{}`)} },
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{}`) },
		[]string{CaseEventSpan}},
	{"event inside the span changes nothing",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_api_query", 9000, `{}`)}
		},
		func() records.Record { return ccEvent("s1", "tengu_api_query", 5000, `{}`) },
		[]string{CaseEventSpan}},
	{"visible call extends the span",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return callEnd(ccCall("c1", "s1", 4000)) },
		[]string{CaseCallVisible}},
	{"hidden call doesn't count",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 4000))) },
		[]string{CaseCallNotVisible}},
	{"call in flight counts",
		func() []records.Record { return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`)} },
		func() records.Record { return callStart(ccCall("c1", "s1", 4000)) },
		[]string{CaseStartVisible}},
	{"hidden call's ending takes back its start",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), callStart(ccCall("c1", "s1", 4000))}
		},
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 4000))) },
		[]string{CaseCallNotVisible}},
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
		[]string{CaseCallVisible}},
	{"a synthetic call doesn't count", nil,
		func() records.Record { c := ccCall("c1", "s1", 4000); c.Synthetic = true; return callEnd(c) },
		[]string{CaseCallNotVisible}},
	{"a Codex turn in flight doesn't count", nil,
		func() records.Record { return callStart(codexCall("x1#1", "s1", "t", 4000)) },
		[]string{CaseStartNotVisible}},
	{"a Codex call is visible in its thread", nil,
		func() records.Record { return callEnd(codexCall("x1", "s1", "t", 4000)) },
		[]string{CaseCallVisible}},
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

func TestSpanAtomic(t *testing.T) {
	set := map[string]bool{CaseOtherRecord: true, CaseEventNotSession: true, CaseEventSpan: true,
		CaseCallVisible: true, CaseCallNotVisible: true, CaseStartVisible: true, CaseStartNotVisible: true, CaseStartAfterEnd: true}
	for _, c := range spanCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{} // a case's events are its batches' whole
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			if got := only(d.Cases, set); !reflect.DeepEqual(got, c.cases) {
				t.Fatalf("cases %v, want %v", got, c.cases)
			}
			if d2 := o.apply(r); !reflect.DeepEqual(d2.Cases, []string{CaseDuplicate}) || len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
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
	for _, cs := range [][]atomicCase{spanCases, eventCases, dimCases, promptCases, requestCases, sessionCases, callToolCases, factCases} {
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

// A checkpoint round trip keeps every row, and a damaged one is an error.
func TestEncodeDecode(t *testing.T) {
	o := stream([]records.Record{ccEvent("s1", "tengu_api_query", 1000, `{}`), ccEvent("s1", "tengu_input_prompt", 3000, `{}`)})
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

// generateSessions is n sessions of about per records each: events (with blur
// and sleep windows of every size, values as strings, zero and negative), visible, hidden and in-flight
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
