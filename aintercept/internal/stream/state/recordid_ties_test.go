package state

import (
	"fmt"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// A tie on time is broken by record id, and record ids sort in emission order
// (records' fixed-width ids), so the later-emitted record wins a "latest" pick
// across 9→10, 99→100 and 999→1000 exactly as it wins between 1 and 2.
func TestTiesFollowEmissionOrder(t *testing.T) {
	inc := records.NewIncarnation("pod")
	id := func(seq int64) string { return fmt.Sprintf("%s-%020d", inc, seq) }
	for _, pair := range [][2]int64{{1, 2}, {9, 10}, {99, 100}, {999, 1000}} {
		batchN = map[string]int64{}
		mk := func(seq int64, n string) records.Record {
			r := inputPrompt("tie", "p1", n, 1000)
			r.RecordID, r.EventKey = id(seq), id(seq)
			r.Event.EventKey = id(seq)
			return r
		}
		first, second := mk(pair[0], "3"), mk(pair[1], "4")
		for _, order := range [][]records.Record{{first, second}, {second, first}} {
			o := stream(order)
			pi, ok := o.rows[TablePromptIndex]["s:tie/pi:p1"].(PromptIndex)
			if !ok {
				t.Fatalf("no prompt index row: %v", o.rows[TablePromptIndex])
			}
			if pi.Index != 4 {
				t.Errorf("seq %d vs %d: index %d, want the later-emitted record's 4", pair[0], pair[1], pi.Index)
			}
		}
	}
}
