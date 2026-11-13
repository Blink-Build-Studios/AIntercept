package observer

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Ingest has no database path (docs/PIPELINE.md §1 step 4): no Postgres
// connection, DSN or login, and no Redis. An outage of either can't touch
// traffic or lose a flag; the workers' tail discovers new files from the
// recent index in S3.
//
// The observer's whole import graph (what an ingest pod runs) holds no
// database client and none of the packages that hold one. The aintercept
// binary carries every command, so this is checked per package, not per binary.
func TestIngestHasNoDatabasePath(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 50 {
		t.Fatalf("go list found only %d packages: %v", len(deps), deps)
	}
	const mod = "github.com/Blink-Build-Studios/aintercept/aintercept/internal/"
	banned := []string{
		"github.com/jackc/",             // pgx
		"github.com/lib/pq",             // another Postgres driver
		"github.com/redis/",             // go-redis
		"github.com/alicebob/miniredis", // Redis in tests
		mod + "pg",                      // the workers' Postgres
		mod + "tail",                    // the workers' discovery
		mod + "rollup",                  // the workers
	}
	for _, d := range deps {
		for _, b := range banned {
			if d == b || strings.HasPrefix(d, b+"/") || (strings.HasSuffix(b, "/") && strings.HasPrefix(d, b)) {
				t.Errorf("ingest imports %s", d)
			}
		}
	}
}

// Nor does ingest read a database setting: no Postgres or Redis variable named
// in the source of the packages an ingest pod runs.
func TestIngestReadsNoDatabaseSetting(t *testing.T) {
	setting := regexp.MustCompile(`"[A-Z0-9_]*(REDIS|POSTGRES|_PG|PGHOST|DSN|DATABASE_URL)[A-Z0-9_]*"`)
	for _, dir := range []string{".", "../keyflag", "../records", "../events", "../extproc"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				if setting.MatchString(line) {
					t.Errorf("%s:%d reads a database setting: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
