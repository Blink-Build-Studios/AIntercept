package state

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The API's SQL (api/src/aintercept_api/sql/session_rows.sql) over the stored
// tables equals the reference's session_rows. It needs DuckDB:
// AINTERCEPT_DUCKDB_PYTHON names a Python with the duckdb package (the API's
// venv), so it runs locally; with AINTERCEPT_RECORDS set it also runs on the
// capture.

const sqlCheck = `
import duckdb, sys
d, sql = sys.argv[1], open(sys.argv[2]).read()
con = duckdb.connect()
for t in ["session_rows", "session_prompt_ids", "session_devices", "session_call_accounts",
          "session_missed", "session_edges", "session_title_calls", "want"]:
    con.execute(f"CREATE TABLE {t} AS SELECT * FROM read_parquet('{d}/{t}.parquet')")
got = con.execute(sql)
cols = [c[0] for c in got.description]
con.execute("CREATE TABLE got AS " + sql)
sel = ", ".join('"' + c + '"' for c in cols)
extra = con.execute(f"SELECT * FROM got EXCEPT SELECT {sel} FROM want").fetchall()
missing = con.execute(f"SELECT {sel} FROM want EXCEPT SELECT * FROM got").fetchall()
n = con.execute("SELECT count(*) FROM want").fetchone()[0]
print(f"{n} rows; {len(extra)} not wanted, {len(missing)} missing")
for r in extra[:2]:
    print("got ", dict(zip(cols, r)))
for r in missing[:2]:
    print("want", dict(zip(cols, r)))
sys.exit(1 if extra or missing else 0)
`

func runSessionSQL(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	py := os.Getenv("AINTERCEPT_DUCKDB_PYTHON")
	dir := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name+".parquet"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(TableSessionRows, process.EncodeParquet(rowsOf[SessionRowK](o, TableSessionRows)))
	write(TableSessionPromptIDs, process.EncodeParquet(rowsOf[SessionPromptID](o, TableSessionPromptIDs)))
	write(TableSessionDevices, process.EncodeParquet(rowsOf[SessionDevice](o, TableSessionDevices)))
	write(TableSessionCallAccounts, process.EncodeParquet(rowsOf[SessionCallAccount](o, TableSessionCallAccounts)))
	write(TableSessionMissed, process.EncodeParquet(rowsOf[SessionMissed](o, TableSessionMissed)))
	write(TableSessionEdges, process.EncodeParquet(rowsOf[SessionEdge](o, TableSessionEdges)))
	write(TableSessionTitleCalls, process.EncodeParquet(rowsOf[SessionTitleCall](o, TableSessionTitleCalls)))
	write("want", process.EncodeParquet(reference(rs).SessionRows))
	script := filepath.Join(dir, "check.py")
	if err := os.WriteFile(script, []byte(sqlCheck), 0o600); err != nil {
		t.Fatal(err)
	}
	sqlPath, _ := filepath.Abs("../../../../api/src/aintercept_api/sql/session_rows.sql")
	out, err := exec.Command(py, "-I", script, dir, sqlPath).CombinedOutput()
	t.Logf("%s: %s", label, out)
	if err != nil {
		t.Fatalf("%s: the SQL's rows differ from the reference: %v", label, err)
	}
}

func TestSessionRowsSQL(t *testing.T) {
	if os.Getenv("AINTERCEPT_DUCKDB_PYTHON") == "" {
		t.Skip("AINTERCEPT_DUCKDB_PYTHON isn't set")
	}
	for seed := int64(1); seed <= 25; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateSessionRows(rng, 250)
		runSessionSQL(t, fmt.Sprintf("seed %d", seed), rs, stream(shuffled(rng, rs, 25)))
	}
	if dir := os.Getenv("AINTERCEPT_RECORDS"); dir != "" {
		rs := readRecords(t, dir)
		runSessionSQL(t, "capture", rs, stream(shuffled(rand.New(rand.NewSource(1)), rs, len(rs)/50)))
	}
}

// readRecords is every record under a records directory.
func readRecords(t *testing.T, dir string) []records.Record {
	t.Helper()
	var rs []records.Record
	err := filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() || filepath.Ext(p) != ".parquet" {
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
