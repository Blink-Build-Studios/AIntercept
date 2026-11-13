package state

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Fixtures for the event rows' SQL (api/src/aintercept_api/sql/events.sql): with
// EVENTS_SQL_OUT set, each scenario's stored event tables and the reference's
// event rows are written as Parquet, and api/tests/test_events_sql.py requires
// the SQL over the stored rows to equal the reference. Real data (with
// AINTERCEPT_RECORDS) stays local, never committed.
func TestWriteEventsSQLFixtures(t *testing.T) {
	out := os.Getenv("EVENTS_SQL_OUT")
	if out == "" {
		t.Skip("EVENTS_SQL_OUT isn't set")
	}
	write := func(name string, rs, in []records.Record) {
		o := stream(in)
		dir := filepath.Join(out, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		put := func(file string, b []byte) {
			if err := os.WriteFile(filepath.Join(dir, file+".parquet"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		put(TableEvents, process.EncodeParquet(stored[Event](o, TableEvents)))
		put(TableRequestLinks, process.EncodeParquet(stored[RequestLink](o, TableRequestLinks)))
		put(TablePromptIndex, process.EncodeParquet(stored[PromptIndex](o, TablePromptIndex)))
		put(TableCodexTurns, process.EncodeParquet(stored[CodexTurn](o, TableCodexTurns)))
		put(TableCallToolUses, process.EncodeParquet(stored[CallToolUse](o, TableCallToolUses)))
		ref := reference(rs)
		put("ref_event_rows", process.EncodeParquet(ref.EventRows))
		put("ref_call_tool_uses", process.EncodeParquet(ref.CallTools))
	}
	for seed := int64(1); seed <= 5; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := append(generateEvents(rng, 3, 200), generateSessions(rng, 2, 100)...)
		write("seed"+itoa(int(seed)), rs, shuffled(rng, rs, 40))
	}
	if dir := os.Getenv("AINTERCEPT_RECORDS"); dir != "" {
		rs := readRecords(t, dir)
		write("capture", rs, shuffled(rand.New(rand.NewSource(1)), rs, len(rs)/50))
	}
}
