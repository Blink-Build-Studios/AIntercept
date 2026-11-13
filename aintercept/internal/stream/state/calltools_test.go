package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// call_tool_uses: the tools each ended call's response invoked (process
// personmap.go), kind "mcp" with its server or "builtin". The stream stores
// one row per call and tool; the query takes the distinct (session, time,
// kind, server, tool), which is the reference's set.

// callToolUsesOf is the stored rows as the query reads them: distinct, ordered.
func callToolUsesOf(o *streamOut) []process.CallToolUse {
	seen := map[process.CallToolUse]bool{}
	var out []process.CallToolUse
	for _, r := range o.rows[TableCallToolUses] {
		x := r.(CallToolUse).CallToolUse
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sortCallTools(out)
	return out
}

func sortCallTools(out []process.CallToolUse) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.SessionID != b.SessionID:
			return a.SessionID < b.SessionID
		case a.TsNS != b.TsNS:
			return a.TsNS < b.TsNS
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.Server != b.Server:
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
}

func checkCallTools(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	want := append([]process.CallToolUse(nil), reference(rs).CallTools...)
	sortCallTools(want)
	if got := callToolUsesOf(o); !(len(got) == 0 && len(want) == 0) && !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: call tool uses differ\n got %+v\nwant %+v", label, got, want)
	}
}

func withTools(c *canon.Request, js string) *canon.Request { c.ToolUsesJSON = js; return c }

var callToolCases = []atomicCase{
	{"a builtin tool", nil,
		func() records.Record {
			return callEnd(withTools(ccCall("c1", "s1", 1000), `[{"kind":"builtin","tool":"Edit"}]`))
		},
		[]string{CaseCallToolUse}},
	{"an MCP tool with its server, and one without (builtin)", nil,
		func() records.Record {
			return callEnd(withTools(ccCall("c1", "s1", 1000), `[{"kind":"mcp","server":"gh","tool":"pr"},{"kind":"mcp","tool":"x"}]`))
		},
		[]string{CaseCallToolUse}},
	{"the same tool twice in a call: one row", nil,
		func() records.Record {
			return callEnd(withTools(ccCall("c1", "s1", 1000), `[{"kind":"builtin","tool":"Read"},{"kind":"builtin","tool":"Read"}]`))
		},
		[]string{CaseCallToolUse}},
	{"a hidden call's tools count", nil,
		func() records.Record {
			return callEnd(withTools(hidden(ccCall("c1", "s1", 1000)), `[{"kind":"builtin","tool":"Bash"}]`))
		},
		[]string{CaseCallToolUse}},
	{"a call in flight: none yet", nil,
		func() records.Record {
			return callStart(withTools(ccCall("c1", "s1", 1000), `[{"kind":"builtin","tool":"Bash"}]`))
		},
		nil},
	{"no tools", nil,
		func() records.Record { return callEnd(ccCall("c1", "s1", 1000)) },
		nil},
	{"the same tool at the same time in two calls: one row in the query", func() []records.Record {
		return []records.Record{callEnd(withTools(ccCall("c1", "s1", 1000), `[{"kind":"builtin","tool":"Edit"}]`))}
	},
		func() records.Record {
			return callEnd(withTools(ccCall("c2", "s1", 1000), `[{"kind":"builtin","tool":"Edit"}]`))
		},
		[]string{CaseCallToolUse}},
}

var callToolCaseSet = map[string]bool{CaseCallToolUse: true}

func TestCallToolUsesAtomic(t *testing.T) {
	for _, c := range callToolCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkCallTools(t, c.name, append(before, r), o)
			if got := only(d.Cases, callToolCaseSet); !reflect.DeepEqual(got, c.cases) {
				t.Fatalf("cases %v, want %v", got, c.cases)
			}
			if d2 := o.apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

func TestCallToolUsesShortSequencesEveryOrder(t *testing.T) {
	mk := func() []records.Record {
		c := withTools(ccCall("c1", "s1", 1000), `[{"kind":"builtin","tool":"Edit"}]`)
		return []records.Record{callStart(c), callEnd(c),
			callEnd(withTools(ccCall("c2", "s1", 1000), `[{"kind":"builtin","tool":"Edit"},{"kind":"mcp","server":"gh","tool":"pr"}]`)),
			callEnd(withTools(ccCall("c3", "s2", 900), `[{"kind":"builtin","tool":"Read"}]`))}
	}
	base := mk()
	permute(len(base), func(order []int) {
		batchN = map[string]int64{}
		rs := mk()
		in := make([]records.Record, len(order))
		for i, k := range order {
			in[i] = rs[k]
		}
		checkCallTools(t, fmt.Sprint(order), rs, stream(in))
	})
}

func TestCallToolUsesGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 20/seedFrac; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := append(generateEvents(rng, 3, 200), generateSessions(rng, 2, 100)...)
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
		checkCallTools(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}
