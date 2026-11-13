package rollup

import (
	"context"
	"sort"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// The merge lists each key's Claude Code and Codex calls' times in its day
// partition (merge_agent_calls), as /overview counts requests over today's
// calls: non-synthetic calls whose product is claude_code or codex (a call to
// api.anthropic.com is Claude Code's when it has none), at their start, else
// their end; and calls in flight with a start, at it (a Codex one by its
// originator). So the directory never reads the fact tables.

func agentCallTimes(t *testing.T, s stream.Store) []int64 {
	t.Helper()
	ctx := context.Background()
	m, err := ReadNewestMerge(ctx, s, "")
	if err != nil || m == nil {
		t.Fatalf("merge commit: %v %v", m, err)
	}
	var out []int64
	for _, o := range m.Days {
		if !contains(o.Tables, TableMergeAgentCalls) {
			continue
		}
		rows, err := readTableFile(ctx, s, MergeDir("")+TableFile(o.Dir, TableMergeAgentCalls), TableMergeAgentCalls)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			out = append(out, r.Value.(MergeAgentCall).TsNS)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func TestMergeListsAgentCallTimes(t *testing.T) {
	base := day(0)
	cases := []struct {
		name string
		reqs []canon.Request
		pend []canon.Pending
		want []int64
	}{
		{"a Claude Code call, at its start", []canon.Request{{CallID: "c1", Product: canon.ProductClaudeCode, StartNS: base + 1, EndNS: base + 9}}, nil, []int64{base + 1}},
		{"a Codex call", []canon.Request{{CallID: "c1", Product: canon.ProductCodex, StartNS: base + 2}}, nil, []int64{base + 2}},
		{"api.anthropic.com with no product", []canon.Request{{CallID: "c1", Host: "api.anthropic.com", StartNS: base + 3}}, nil, []int64{base + 3}},
		{"no start: its end", []canon.Request{{CallID: "c1", Product: canon.ProductCodex, EndNS: base + 4}}, nil, []int64{base + 4}},
		{"another product", []canon.Request{{CallID: "c1", Host: "chatgpt.com", StartNS: base + 5}}, nil, nil},
		{"synthetic", []canon.Request{{CallID: "c1", Product: canon.ProductClaudeCode, Synthetic: true, StartNS: base + 6}}, nil, nil},
		{"in flight to api.anthropic.com", nil, []canon.Pending{{CorrelationID: "p1", Host: "api.anthropic.com", SeenNS: base + 7, StartNS: base + 7}}, []int64{base + 7}},
		{"in flight, Codex by its originator", nil, []canon.Pending{{CorrelationID: "p1", Host: "chatgpt.com", ClientJSON: `{"originator":"codex_cli_rs"}`, SeenNS: base + 8, StartNS: base + 8}}, []int64{base + 8}},
		{"in flight, another originator", nil, []canon.Pending{{CorrelationID: "p1", Host: "chatgpt.com", ClientJSON: `{"originator":"other"}`, SeenNS: base + 8, StartNS: base + 8}}, nil},
		{"in flight with no start", nil, []canon.Pending{{CorrelationID: "p1", Host: "api.anthropic.com", SeenNS: base + 9}}, nil},
		{"done", nil, []canon.Pending{{CorrelationID: "p1", Host: "api.anthropic.com", Done: true, SeenNS: base + 9, StartNS: base + 9}}, nil},
		{"in flight, synthetic", nil, []canon.Pending{{CorrelationID: "p1", Host: "api.anthropic.com", Synthetic: true, SeenNS: base + 9, StartNS: base + 9}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := stream.NewMemStore(stream.RealClock{})
			factKey(t, s, "s:a", c.reqs, c.pend, nil, 0)
			run(t, s, newQueue("s:a"))
			got := agentCallTimes(t, s)
			if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
				t.Fatalf("agent call times %v, want %v", got, c.want)
			}
		})
	}
}

// Each key's calls, kept when a later run merges other keys into the partition.
func TestMergeAgentCallsFollowTheirKeys(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	cc := func(id string, ns int64) canon.Request {
		return canon.Request{CallID: id, Product: canon.ProductClaudeCode, StartNS: ns}
	}
	factKey(t, s, "s:a", []canon.Request{cc("c1", day(0)+1)}, nil, nil, 0)
	factKey(t, s, "s:b", []canon.Request{cc("c2", day(0)+2)}, nil, nil, 0)
	run(t, s, newQueue("s:a", "s:b"))
	factKey(t, s, "s:c", []canon.Request{cc("c3", day(0)+3)}, nil, nil, 0)
	run(t, s, newQueue("s:c"))
	got := agentCallTimes(t, s)
	want := []int64{day(0) + 1, day(0) + 2, day(0) + 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("agent call times %v, want %v", got, want)
	}
}
