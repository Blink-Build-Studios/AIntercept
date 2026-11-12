package state

import (
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Fixtures for the query's SQL (api/src/aintercept_api/sql/prompts.sql): with
// PROMPTS_SQL_OUT set, each case's stored rows and the reference's rows are
// written as Parquet, and api/tests/test_prompts_sql.py requires the SQL's
// views over the stored rows to equal the reference. Real data (with
// AINTERCEPT_RECORDS) is local only, never committed.
func TestPromptsSQLFixtures(t *testing.T) {
	out := os.Getenv("PROMPTS_SQL_OUT")
	if out == "" {
		t.Skip("PROMPTS_SQL_OUT isn't set")
	}
	write := func(name string, rs []records.Record, o *streamOut) {
		dir := filepath.Join(out, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		put := func(file string, b []byte) {
			if err := os.WriteFile(filepath.Join(dir, file+".parquet"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		put(TablePromptItems, process.EncodeParquet(stored[PromptItem](o, TablePromptItems)))
		put(TablePromptSteps, process.EncodeParquet(stored[StepRow](o, TablePromptSteps)))
		put(TablePromptSession, process.EncodeParquet(stored[PromptSession](o, TablePromptSession)))
		put(TableMarks, process.EncodeParquet(stored[MarkRow](o, TableMarks)))
		put(TableMarkPromptIndex, process.EncodeParquet(stored[MarkPromptIndex](o, TableMarkPromptIndex)))
		r, st, m := refPrompts(reference(rs))
		put("ref_prompt_rows", process.EncodeParquet(r))
		put("ref_prompt_steps", process.EncodeParquet(st))
		put("ref_session_marks", process.EncodeParquet(m))
	}
	for seed := int64(1); seed <= 5; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generatePrompts(rng, 3, 200)
		write("seed"+itoa(int(seed)), rs, stream(shuffled(rng, rs, 40)))
	}
	if dir := os.Getenv("AINTERCEPT_RECORDS"); dir != "" {
		rs := readRecords(t, dir)
		write("capture", rs, stream(shuffled(rand.New(rand.NewSource(1)), rs, len(rs)/50)))
	}
}

// stored is a table's rows, in a fixed order.
func stored[T any](o *streamOut, table string) []T {
	keys := make([]string, 0, len(o.rows[table]))
	for k := range o.rows[table] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		out = append(out, o.rows[table][k].(T))
	}
	return out
}

// readRecords is every record in a records directory.
func readRecords(t *testing.T, dir string) []records.Record {
	t.Helper()
	var rs []records.Record
	err := filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || !strings.HasSuffix(p, ".parquet") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		x, err := records.Decode(b)
		rs = append(rs, x...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}
