package pg_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
	// Every table has exactly its columns, in order, with their types.
	for _, tb := range pg.Tables {
		rows, err := p.Query(c, `SELECT attname, format_type(atttypid, atttypmod) FROM pg_attribute
			WHERE attrelid = $1::text::regclass AND attnum > 0 AND NOT attisdropped ORDER BY attnum`, tb.Name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
			var n, ty string
			err := r.Scan(&n, &ty)
			return n + " " + ty, err
		})
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, col := range tb.Columns {
			ty, _, _ := strings.Cut(col.Type, " NOT NULL")
			if ty == "timestamptz" {
				ty = "timestamp with time zone"
			}
			want = append(want, col.Name+" "+ty)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s:\n got %v\nwant %v", tb.Name, got, want)
		}
	}
	var lz4 string
	if err := p.QueryRow(c, "SELECT attcompression FROM pg_attribute WHERE attrelid = 'analytics'::regclass AND attname = 'metadata_json'").Scan(&lz4); err != nil || lz4 != "l" {
		t.Errorf("analytics.metadata_json compression %q, %v", lz4, err)
	}
	var tailRows int
	if err := p.QueryRow(c, "SELECT count(*) FROM tail").Scan(&tailRows); err != nil || tailRows != 1 {
		t.Errorf("tail has %d rows, %v", tailRows, err)
	}
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

func TestByteaIDsRoundTrip(t *testing.T) {
	// The state's row ids join their parts with NUL: they're bytea, and come back byte for byte.
	p := applied(t)
	c := ctx(t)
	long := bytes.Repeat([]byte("s\x00"), 300)
	ids := [][]byte{[]byte("s1"), []byte("s1\x00p1"), []byte("s1\x00p2"), []byte("\x00"), []byte(""), long, []byte("é\x00\xff\xfe")}
	for i, id := range ids {
		if _, err := p.Exec(c, "INSERT INTO dim_session_visible (key, id, session_id) VALUES ('s:k', $1, $2)", id, string(rune('a'+i))); err != nil {
			t.Fatalf("insert %q: %v", id, err)
		}
	}
	rows, _ := p.Query(c, "SELECT id, session_id FROM dim_session_visible WHERE key = 's:k' ORDER BY session_id")
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([]byte, error) {
		var id []byte
		var s string
		return id, r.Scan(&id, &s)
	})
	if err != nil || len(got) != len(ids) {
		t.Fatalf("read %d ids: %v", len(got), err)
	}
	for i := range ids {
		if !bytes.Equal(got[i], ids[i]) {
			t.Errorf("id %d: %q, want %q", i, got[i], ids[i])
		}
	}
	// The ids are distinct keys: the same one again conflicts, by its bytes.
	if _, err := p.Exec(c, "INSERT INTO dim_session_visible (key, id) VALUES ('s:k', $1)", []byte("s1\x00p1")); sqlState(err) != "23505" {
		t.Errorf("a repeated id: %v, want a unique violation", err)
	}
	// And the upsert-by-unnest the workers write with keeps them apart too.
	if _, err := p.Exec(c, `INSERT INTO dim_session_visible (key, id, session_id) SELECT 's:k', * FROM unnest($1::bytea[], $2::text[])
		ON CONFLICT (key, id) DO UPDATE SET session_id = excluded.session_id`, [][]byte{[]byte("s1\x00p1"), []byte("s1\x00p3")}, []string{"x", "y"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := p.QueryRow(c, "SELECT count(*) FROM dim_session_visible WHERE key = 's:k'").Scan(&n); err != nil || n != len(ids)+1 {
		t.Errorf("%d rows after the upsert, %v", n, err)
	}
}

func TestSanitizedTextIsStorable(t *testing.T) {
	// Why canon cleans what it does: Postgres text refuses NUL, and an index
	// entry over about 2.7 KB fails the insert. The cleaned values store.
	p := applied(t)
	c := ctx(t)
	insert := func(callID, path string) error {
		_, err := p.Exec(c, "INSERT INTO requests (key, id, call_id, path) VALUES ('s:k', $1, $2, $3)", []byte(callID), callID, path)
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
	if _, err := p.Exec(c, "UPDATE requests SET call_id = $1 WHERE call_id = 'c2'", huge); err == nil {
		t.Error("an 8 KB indexed id was stored")
	}
	if err := insert(canon.CleanID(huge), ""); err != nil {
		t.Errorf("the capped id: %v", err)
	}
	var got string
	if err := p.QueryRow(c, "SELECT path FROM requests WHERE call_id = 'c1'").Scan(&got); err != nil || got != "/v1/messages\uFFFD" {
		t.Errorf("read back %q, %v", got, err)
	}
}

func TestCollationIsCodePointOrder(t *testing.T) {
	// The built-in C.UTF-8 locale sorts by code point, which is UTF-8 byte
	// order: Go's and DuckDB's, so min(name) and ORDER BY agree everywhere.
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
	// lower() is Unicode's simple case mapping (the API's tests check it against DuckDB's on every code point).
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
	if _, err := worker.Exec(c, "INSERT INTO keys (key, n) VALUES ('s:1', 1)"); err != nil {
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
	for _, tb := range pg.Tables {
		if _, err := api.Exec(c, "SELECT * FROM "+tb.Name+" LIMIT 1"); err != nil {
			t.Errorf("the API reads %s: %v", tb.Name, err)
		}
	}
	if _, err := api.Exec(c, "INSERT INTO keys (key, n) VALUES ('s:2', 1)"); sqlState(err) != "42501" {
		t.Errorf("the API wrote: %v", err)
	}
	if _, err := api.Exec(c, "CREATE TABLE x (y int)"); sqlState(err) != "42501" {
		t.Errorf("the API created a table: %v", err)
	}
}
