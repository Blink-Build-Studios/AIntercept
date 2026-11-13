package rollup

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// v7's agent_calls (render) are the merge's merge_agent_calls: the same calls
// of a key's fact rows, at the same times (0 there is NULL here).
func TestRenderedAgentCallsAreTheMerges(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	pick := func(xs ...string) string { return xs[rng.Intn(len(xs))] }
	for round := 0; round < 200; round++ {
		key := "s:a"
		rows := state.Rows{state.TableFactRequests: {}, state.TableFactPending: {}}
		tables := map[string][]stream.Row{}
		for i := 0; i < 6; i++ {
			id := fmt.Sprintf("c%d", rng.Intn(8))
			if rng.Intn(2) == 0 {
				r := canon.Request{CallID: id, Product: pick("", canon.ProductClaudeCode, canon.ProductCodex, "other"),
					Host: pick("api.anthropic.com", "chatgpt.com"), Synthetic: rng.Intn(5) == 0,
					StartNS: int64(rng.Intn(2)) * 1000, EndNS: int64(rng.Intn(2)) * 2000}
				_, ended := rows[state.TableFactRequests][id]
				if _, inFlight := rows[state.TableFactPending][id]; ended || inFlight {
					continue // a key never holds a call both ended and in flight
				}
				rows[state.TableFactRequests][id] = r
				tables[state.TableFactRequests] = append(tables[state.TableFactRequests], stream.Row{Key: key, ID: id, Value: r})
				continue
			}
			_, ended := rows[state.TableFactRequests][id]
			if _, inFlight := rows[state.TableFactPending][id]; ended || inFlight {
				continue
			}
			p := canon.Pending{CorrelationID: id, Host: pick("api.anthropic.com", "chatgpt.com"),
				ClientJSON: pick("", `{"originator":"codex_cli_rs"}`, `{"originator":"other"}`, `not json`),
				Synthetic:  rng.Intn(5) == 0, Done: rng.Intn(5) == 0, StartNS: int64(rng.Intn(2)) * 3000}
			rows[state.TableFactPending][id] = p
			tables[state.TableFactPending] = append(tables[state.TableFactPending], stream.Row{Key: key, ID: id, Value: p})
		}
		var want []string
		for _, r := range agentCalls(key, tables) {
			want = append(want, fmt.Sprintf("%s %d", r.ID[2:], r.Value.(MergeAgentCall).TsNS))
		}
		r, err := render.Render(key, rows, "")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, td := range r.Diff(nil).Tables {
			if td.Table.Name != "agent_calls" {
				continue
			}
			for _, row := range td.Upserts {
				ts, _ := row[2].(int64)
				got = append(got, fmt.Sprintf("%s %d", row[1], ts))
			}
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: agent_calls %v, the merge's %v", round, got, want)
		}
	}
}
