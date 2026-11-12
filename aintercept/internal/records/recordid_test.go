package records

import (
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Record ids are compared as strings wherever a tie is broken by (ts, record id):
// in the stream state, in its SQL and in the API. So string order must be
// emission order: within an incarnation across 9→10, 99→100, …, and across
// incarnations by when they started.
func TestRecordIDsSortInEmissionOrder(t *testing.T) {
	inc := NewIncarnation("observer-abc-1")
	if !regexp.MustCompile(`^\d{19}-[0-9a-f]{8}-observer-abc-1$`).MatchString(inc) {
		t.Fatalf("incarnation %q: want <19-digit start ns>-<8 hex>-<pod>", inc)
	}
	a := NewAssembler(inc)
	var ids []string
	for i := 0; i < 1005; i++ {
		e := events.Event{CorrelationID: "c", Phase: events.PhaseVerdict, TimestampNS: int64(i)}
		rs := a.Apply(&e, nil, "f")
		for _, r := range rs {
			ids = append(ids, r.RecordID)
		}
	}
	if len(ids) < 1001 {
		t.Fatalf("only %d records", len(ids))
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(inc) + `-\d{20}$`)
	for _, id := range ids {
		if !re.MatchString(id) {
			t.Fatalf("record id %q: want <incarnation>-<20-digit seq>", id)
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("record ids don't sort lexically in emission order")
	}
	time.Sleep(time.Millisecond)
	later := NewIncarnation("a") // a shorter, lexically smaller pod name started later
	if !(later > inc) {
		t.Fatalf("a later incarnation %q sorts before %q", later, inc)
	}
}
