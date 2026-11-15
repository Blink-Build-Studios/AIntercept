package pg_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
)

// Postgres integration (docs/PIPELINE.md, Testing): on the test server, each
// test in its own fresh database.

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return c
}

func open(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	p, err := pg.Open(ctx(t), dsn, 4)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func applied(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p := open(t, pgtest.Fresh(t))
	if err := pg.Apply(ctx(t), p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return p
}

func sqlState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestSchemaAppliesOnce(t *testing.T) {
	p := open(t, pgtest.Fresh(t))
	c := ctx(t)
	if err := pg.Check(c, p); !errors.Is(err, pg.ErrNoSchema) {
		t.Fatalf("check before: %v", err)
	}
	// Workers starting together: one applies it, the rest find it applied.
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = pg.Apply(c, p) }()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	var hashes []string
	if err := p.QueryRow(c, "SELECT array_agg(hash) FROM schema_version").Scan(&hashes); err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 || hashes[0] != pg.SchemaHash || len(pg.SchemaHash) != 64 {
		t.Fatalf("schema_version %v, want [%s]", hashes, pg.SchemaHash)
	}
	if err := pg.Check(c, p); err != nil {
		t.Fatalf("check after: %v", err)
	}
	// The schema's tables, and nothing else.
	var tables []string
	if err := p.QueryRow(c, "SELECT array_agg(tablename::text ORDER BY tablename) FROM pg_tables WHERE schemaname = current_schema()").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tables, " "); got != strings.Join(schemaTables(), " ") {
		t.Errorf("tables %s, want %s", got, strings.Join(schemaTables(), " "))
	}
}

// schemaTables is the tables schema.sql creates, sorted.
func schemaTables() []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^CREATE TABLE (\w+)`).FindAllStringSubmatch(pg.Schema, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func TestHashMismatchRefusesToStart(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	if _, err := p.Exec(c, "UPDATE schema_version SET hash = 'an older build'"); err != nil {
		t.Fatal(err)
	}
	if err := pg.Apply(c, p); !errors.Is(err, pg.ErrSchemaMismatch) {
		t.Errorf("apply: %v, want ErrSchemaMismatch", err)
	}
	if err := pg.Check(c, p); !errors.Is(err, pg.ErrSchemaMismatch) {
		t.Errorf("check: %v, want ErrSchemaMismatch", err)
	}
}

func TestApplyNeedsAnEmptyDatabase(t *testing.T) {
	p := open(t, pgtest.Fresh(t))
	c := ctx(t)
	if _, err := p.Exec(c, "CREATE TABLE something_else (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := pg.Apply(c, p); !errors.Is(err, pg.ErrNotEmpty) {
		t.Fatalf("apply: %v, want ErrNotEmpty", err)
	}
}

func TestSanitizedTextIsStorable(t *testing.T) {
	// Why canon cleans what it does: Postgres text refuses NUL, and an index
	// entry over about 2.7 KB fails the insert. The cleaned values store.
	p := applied(t)
	c := ctx(t)
	insert := func(callID, path string) error {
		_, err := p.Exec(c, "INSERT INTO calls (call_id, correlation_id, kind, stream, synthetic, path) VALUES ($1, $1, 'model', false, false, $2)", callID, path)
		return err
	}
	if err := insert("c1", "/v1/messages\x00"); err == nil {
		t.Error("text with NUL was stored")
	}
	if err := insert("c1", canon.CleanText("/v1/messages\x00")); err != nil {
		t.Errorf("cleaned text: %v", err)
	}
	b := make([]byte, 6<<10)
	rand.Read(b)
	huge := base64.StdEncoding.EncodeToString(b) // incompressible, ~8 KB
	if err := insert("c2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(c, "UPDATE calls SET call_id = $1 WHERE call_id = 'c2'", huge); err == nil {
		t.Error("an 8 KB indexed id was stored")
	}
	if err := insert(canon.CleanID(huge), ""); err != nil {
		t.Errorf("the capped id: %v", err)
	}
	var got string
	if err := p.QueryRow(c, "SELECT path FROM calls WHERE call_id = 'c1'").Scan(&got); err != nil || got != "/v1/messages\uFFFD" {
		t.Errorf("read back %q, %v", got, err)
	}
}

func TestCollationIsCodePointOrder(t *testing.T) {
	// The built-in C.UTF-8 locale sorts by code point, which is UTF-8 byte
	// order: Go's and Python's, so min(name) and ORDER BY agree everywhere.
	p := applied(t)
	c := ctx(t)
	var provider, locale string
	if err := p.QueryRow(c, "SELECT datlocprovider::text, datlocale FROM pg_database WHERE datname = current_database()").Scan(&provider, &locale); err != nil {
		t.Fatal(err)
	}
	if provider != "b" || locale != "C.UTF-8" {
		t.Fatalf("the database's locale is %s/%s, want the built-in C.UTF-8", provider, locale)
	}
	words := []string{"b", "B", "a", "_", "a b", "ab", "é", "e\u0301", "z", "Z", "ä", "ß", "ss", "İ", "i", "日本", "日", "\uFFFD",
		"\U0001F600", "\uFFEE", "", " ", "-", "a-b", "a_b", "aB", "Ab", "sha256:ff", "s:1", "s:10", "s:2", "\u00a0", "\t"}
	var got []string
	if err := p.QueryRow(c, "SELECT array_agg(w ORDER BY w) FROM unnest($1::text[]) w", words).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), words...)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ORDER BY:\n got %q\nwant %q", got, want)
	}
	var lo, hi string
	if err := p.QueryRow(c, "SELECT min(w), max(w) FROM unnest($1::text[]) w", words).Scan(&lo, &hi); err != nil || lo != want[0] || hi != want[len(want)-1] {
		t.Errorf("min %q max %q, want %q %q (%v)", lo, hi, want[0], want[len(want)-1], err)
	}
	// lower() is Unicode's simple case mapping.
	var l string
	if err := p.QueryRow(c, "SELECT lower('ÀÉ İ Σ ẞ ǅ ABC')").Scan(&l); err != nil || l != "àé i σ ß ǆ abc" {
		t.Errorf("lower: %q, %v", l, err)
	}
}

func TestLogins(t *testing.T) {
	admin := pgtest.Fresh(t)
	c := ctx(t)
	conn, err := pgx.Connect(c, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(c)
	if err := pg.CreateLogins(c, conn, "worker pw", "api pw"); err != nil {
		t.Fatalf("logins: %v", err)
	}
	if err := pg.CreateLogins(c, conn, "worker pw", "api pw"); err != nil {
		t.Fatalf("logins again: %v", err)
	}
	// Every DSN carries the keepalives (§6); pgx would send unknown parameters to the server.
	ka := "&keepalives_idle=10&keepalives_interval=5&keepalives_count=3&tcp_user_timeout=30000"
	worker := open(t, pgtest.WithUser(t, admin, pg.WorkerLogin, "worker pw")+ka)
	if err := pg.Apply(c, worker); err != nil {
		t.Fatalf("the worker applies the schema: %v", err)
	}
	show := func(p *pgxpool.Pool, setting string) string {
		var v string
		if err := p.QueryRow(c, "SELECT current_setting($1)", setting).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for setting, want := range map[string]string{"idle_in_transaction_session_timeout": "30s", "lock_timeout": "5s", "statement_timeout": "1min"} {
		if got := show(worker, setting); got != want {
			t.Errorf("worker %s = %s, want %s", setting, got, want)
		}
	}
	if _, err := worker.Exec(c, "INSERT INTO ingest_pods (incarnation) VALUES ('pod-1')"); err != nil {
		t.Errorf("the worker writes: %v", err)
	}
	api := open(t, pgtest.WithUser(t, admin, pg.APILogin, "api pw")+ka)
	for setting, want := range map[string]string{"idle_in_transaction_session_timeout": "30s", "statement_timeout": "30s"} {
		if got := show(api, setting); got != want {
			t.Errorf("api %s = %s, want %s", setting, got, want)
		}
	}
	if err := pg.Check(c, api); err != nil {
		t.Errorf("the API checks the schema: %v", err)
	}
	for _, tb := range schemaTables() {
		if _, err := api.Exec(c, "SELECT * FROM "+tb+" LIMIT 1"); err != nil {
			t.Errorf("the API reads %s: %v", tb, err)
		}
	}
	if _, err := api.Exec(c, "INSERT INTO ingest_pods (incarnation) VALUES ('pod-2')"); sqlState(err) != "42501" {
		t.Errorf("the API wrote: %v", err)
	}
	if _, err := api.Exec(c, "CREATE TABLE x (y int)"); sqlState(err) != "42501" {
		t.Errorf("the API created a table: %v", err)
	}
}
