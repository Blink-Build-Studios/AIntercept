package pg_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
)

// Versioned migrations (docs/PIPELINE.md §3.4): the chain in
// migrations/NNNN_<name>.sql, each in its own transaction under schemaLock.

// v062Hash is the hash v0.6.2 recorded in schema_version: its schema.sql's
// sha256, which the live databases hold.
const v062Hash = "79b14d4e95fdfafe7a2e1f60b76432e39e25bbe551e63572b346c09313a19bbe"

func TestTheFirstMigrationIsV062sSchema(t *testing.T) {
	first := pg.Migrations[0]
	sum := sha256.Sum256([]byte(first.SQL))
	if first.Version != 1 || hex.EncodeToString(sum[:]) != v062Hash {
		t.Fatalf("%s: version %d, hash %x; want version 1 with v0.6.2's schema.sql byte for byte", first.Name, first.Version, sum)
	}
	if pg.Latest != pg.Migrations[len(pg.Migrations)-1].Version {
		t.Fatalf("Latest %d isn't the newest migration's", pg.Latest)
	}
}

func TestParseMigration(t *testing.T) {
	m, err := pg.ParseMigration("0007_rebuild_steps.sql", "-- Steps keep their work.\n-- reload: steps, prompts ,step_keys\n-- breaks readers\nALTER TABLE steps ADD COLUMN x text;\n")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 7 || m.Name != "0007_rebuild_steps.sql" || m.Reset || !m.BreaksReaders ||
		strings.Join(m.Reload, " ") != "steps prompts step_keys" || !strings.Contains(m.SQL, "ALTER TABLE") {
		t.Fatalf("%+v", m)
	}
	m, err = pg.ParseMigration("0002_add.sql", "-- reset\nCREATE TABLE x (y int);\n-- reload: calls (after the first statement: a comment)\n")
	if err != nil || !m.Reset || m.Reload != nil || m.BreaksReaders {
		t.Fatalf("%+v, %v", m, err)
	}
	for _, bad := range [][2]string{
		{"2_add.sql", "SELECT 1;"},
		{"0002-add.sql", "SELECT 1;"},
		{"0002_add.txt", "SELECT 1;"},
		{"0000_zero.sql", "SELECT 1;"},
		{"0002_add.sql", "-- reload:\nSELECT 1;"},
		{"0002_add.sql", "-- reload: calls; DROP TABLE x\nSELECT 1;"},
	} {
		if _, err := pg.ParseMigration(bad[0], bad[1]); err == nil {
			t.Errorf("%s %q parsed", bad[0], bad[1])
		}
	}
}

func TestTheChainIsNumberedInOrder(t *testing.T) {
	a, b := migration(t, 1, "a", "SELECT 1;"), migration(t, 3, "b", "SELECT 1;")
	p := open(t, pgtest.Fresh(t))
	if _, err := pg.Migrate(ctx(t), p, chain(a, b), false); err == nil {
		t.Fatal("a chain with a gap (2, 4) was applied")
	}
	if _, err := pg.Migrate(ctx(t), p, chain(a, a), false); err == nil {
		t.Fatal("a chain with 2 twice was applied")
	}
}

// chain is this build's migrations and then more, a test's own.
func chain(more ...pg.Migration) []pg.Migration {
	return append(slices.Clone(pg.Migrations), more...)
}

// migration is a test's migration, numbered after this build's.
func migration(t *testing.T, n int, name, sql string) pg.Migration {
	t.Helper()
	m, err := pg.ParseMigration(fmt.Sprintf("%04d_%s.sql", pg.Latest+n, name), sql)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The chain applied to an empty database builds schema.sql's schema: the same
// tables, columns (in order, with their types, collations, defaults and
// identities), constraints and indexes.
func TestTheChainBuildsSchemaSQL(t *testing.T) {
	p := applied(t)
	q := open(t, pgtest.Fresh(t))
	if _, err := q.Exec(ctx(t), schemaSQL(t)); err != nil {
		t.Fatalf("schema.sql: %v", err)
	}
	sameCatalog(t, catalog(t, p), catalog(t, q))
}

// A database as v0.6.2 left it (the live deployment's: its schema's hash in
// schema_version, rows in every table) becomes version 1 in place, through
// the workers' login with the reset on (the chart's default): nothing reset,
// every row kept, ingest_files untouched (no reload), and the API's login
// reads the new schema_version. Later migrations in the chain keep the rows
// too, except the tables they declare reloaded.
func TestAV062DatabaseBecomesVersion1InPlace(t *testing.T) {
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
	worker := open(t, pgtest.WithUser(t, admin, pg.WorkerLogin, "worker pw"))
	v062(t, worker)
	before := rows(t, worker, nil)

	a, err := pg.Migrate(c, worker, pg.Migrations, true)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if a.Reset || a.From != 1 || a.To != pg.Latest {
		t.Fatalf("migrate: %+v; want from 1 to %d with no reset", a, pg.Latest)
	}
	want := before
	emptied := map[string]bool{}
	for _, m := range pg.Migrations[1:] {
		for _, tb := range m.Reload {
			emptied[tb], emptied["ingest_files"] = true, true
		}
		if m.Reset {
			for tb := range before {
				emptied[tb] = true
			}
		}
	}
	if len(emptied) > 0 {
		want = map[string][]string{}
		for tb, rs := range before {
			if !emptied[tb] {
				want[tb] = rs
			}
		}
	}
	sameRows(t, want, rows(t, worker, before))
	if got := versions(t, worker); got != versionList(pg.Latest) {
		t.Errorf("schema_version %s, want %s", got, versionList(pg.Latest))
	}
	q := open(t, pgtest.Fresh(t))
	if _, err := q.Exec(c, schemaSQL(t)); err != nil {
		t.Fatal(err)
	}
	sameCatalog(t, catalog(t, worker), catalog(t, q))
	api := open(t, pgtest.WithUser(t, admin, pg.APILogin, "api pw"))
	if err := pg.Check(c, api); err != nil {
		t.Errorf("the API checks the schema: %v", err)
	}
}

// An additive migration (a column, an index, a table) keeps every row and
// reloads nothing.
func TestAnAdditiveMigrationKeepsEveryRow(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	fill(t, p)
	before := rows(t, p, nil)
	add := migration(t, 1, "add", `
ALTER TABLE calls ADD COLUMN note text;
CREATE INDEX ON calls (note);
CREATE TABLE notes (id text PRIMARY KEY);`)
	a, err := pg.Migrate(c, p, chain(add), true)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reset || a.Reload != nil || a.From != pg.Latest || a.To != add.Version {
		t.Fatalf("%+v", a)
	}
	sameRows(t, before, rows(t, p, before))
	if got := versions(t, p); got != versionList(add.Version) {
		t.Fatalf("schema_version %s", got)
	}
	// Again: nothing to do.
	if a, err := pg.Migrate(c, p, chain(add), true); err != nil || a.From != add.Version || a.To != add.Version {
		t.Fatalf("again: %+v, %v", a, err)
	}
}

// A migration that says `-- reload: steps, prompts` empties those tables
// (before it runs, so it can change them as an empty table allows) and
// ingest_files, so the workers reload every raw file; every other table keeps
// its rows.
func TestAReloadMigrationEmptiesOnlyItsTables(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	fill(t, p)
	before := rows(t, p, nil)
	reload := migration(t, 1, "rebuild_steps", `-- Steps get a column their refresh fills.
-- reload: steps, prompts
ALTER TABLE steps ADD COLUMN weight bigint NOT NULL;`)
	a, err := pg.Migrate(c, p, chain(reload), false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reset || strings.Join(a.Reload, " ") != "steps prompts" {
		t.Fatalf("%+v", a)
	}
	want := map[string][]string{}
	for tb, rs := range before {
		if tb != "steps" && tb != "prompts" && tb != "ingest_files" {
			want[tb] = rs
		}
	}
	sameRows(t, want, rows(t, p, before))
}

// A `-- reset` migration empties the database and applies the chain up to it;
// so does the operator's `aintercept pg reset` (pg.Reset), with this build's.
func TestAResetMigrationEmptiesTheDatabase(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	fill(t, p)
	before := rows(t, p, nil)
	reset := migration(t, 1, "start_over", "-- reset\nCREATE TABLE notes (id text PRIMARY KEY);")
	add := migration(t, 2, "add", "ALTER TABLE notes ADD COLUMN body text;")
	a, err := pg.Migrate(c, p, chain(reset, add), false)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Reset || a.To != add.Version {
		t.Fatalf("%+v", a)
	}
	sameRows(t, map[string][]string{}, rows(t, p, before))
	if got := versions(t, p); got != versionList(add.Version) {
		t.Fatalf("schema_version %s", got)
	}
	var cols int
	if err := p.QueryRow(c, "SELECT count(*) FROM pg_attribute WHERE attrelid = 'notes'::regclass AND attnum > 0").Scan(&cols); err != nil || cols != 2 {
		t.Fatalf("notes has %d columns (%v), want 2", cols, err)
	}

	q := applied(t)
	fill(t, q)
	if err := pg.Reset(c, q); err != nil {
		t.Fatal(err)
	}
	sameRows(t, map[string][]string{}, rows(t, q, before))
	if err := pg.Check(c, q); err != nil {
		t.Fatal(err)
	}
}

// A database newer than the build (a rollback) is refused, with the reset on
// or off, and nothing in it changes.
func TestARollbackIsRefusedAndNothingReset(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	newer := migration(t, 1, "newer", "CREATE TABLE notes (id text PRIMARY KEY);")
	if _, err := pg.Migrate(c, p, chain(newer), false); err != nil {
		t.Fatal(err)
	}
	fill(t, p)
	before := rows(t, p, nil)
	for _, resetting := range []bool{false, true} {
		_, err := pg.Migrate(c, p, pg.Migrations, resetting)
		if !errors.Is(err, pg.ErrNewer) || !strings.Contains(err.Error(), fmt.Sprint(newer.Version)) {
			t.Fatalf("reset %v: %v, want ErrNewer naming version %d", resetting, err, newer.Version)
		}
	}
	if err := pg.Check(c, p); !errors.Is(err, pg.ErrNewer) {
		t.Fatalf("check: %v", err)
	}
	sameRows(t, before, rows(t, p, before))
	if got := versions(t, p); got != versionList(newer.Version) {
		t.Fatalf("schema_version %s", got)
	}
}

// Workers starting together run each migration once: on an empty database and
// on a v0.6.2 one.
func TestConcurrentWorkersRunEachMigrationOnce(t *testing.T) {
	for _, start := range []string{"empty", "v0.6.2"} {
		t.Run(start, func(t *testing.T) {
			p := open(t, pgtest.Fresh(t))
			c := ctx(t)
			if start == "v0.6.2" {
				v062(t, p)
			}
			ms := chain(
				migration(t, 1, "ran", "CREATE TABLE ran (n int);\nINSERT INTO ran VALUES (1);"),
				migration(t, 2, "again", "-- reload: calls\nINSERT INTO ran VALUES (2);"),
				migration(t, 3, "more", "INSERT INTO ran VALUES (3);"),
			)
			var wg sync.WaitGroup
			errs := make([]error, 8)
			for i := range errs {
				wg.Add(1)
				go func() { defer wg.Done(); _, errs[i] = pg.Migrate(c, p, ms, true) }()
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("worker %d: %v", i, err)
				}
			}
			var ran []int32
			if err := p.QueryRow(c, "SELECT array_agg(n ORDER BY n) FROM ran").Scan(&ran); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(ran) != "[1 2 3]" {
				t.Fatalf("ran %v, want each migration once", ran)
			}
			if got := versions(t, p); got != versionList(ms[len(ms)-1].Version) {
				t.Fatalf("schema_version %s", got)
			}
		})
	}
}

// Each version records the oldest build whose API reads it: a migration that
// says `-- breaks readers` raises it to its own version, the others keep it.
func TestReadersFrom(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	ms := chain(
		migration(t, 1, "add", "CREATE TABLE notes (id text PRIMARY KEY);"),
		migration(t, 2, "rename", "-- breaks readers\nALTER TABLE notes RENAME COLUMN id TO note_id;"),
		migration(t, 3, "add_more", "ALTER TABLE notes ADD COLUMN body text;"),
	)
	if _, err := pg.Migrate(c, p, ms, false); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := p.QueryRow(c, "SELECT string_agg(version || ':' || readers_from, ' ' ORDER BY version) FROM schema_version").Scan(&got); err != nil {
		t.Fatal(err)
	}
	l := pg.Latest
	ours, from := readersFromList()
	want := fmt.Sprintf("%s %d:%d %d:%d %d:%d", ours, l+1, from, l+2, l+2, l+3, l+2)
	if got != want {
		t.Fatalf("schema_version %s, want %s", got, want)
	}
}

// readersFromList is this build's chain's version:readers_from pairs, and its
// newest's readers_from.
func readersFromList() (string, int) {
	var out []string
	from := 1
	for _, m := range pg.Migrations {
		if m.BreaksReaders {
			from = m.Version
		}
		out = append(out, fmt.Sprintf("%d:%d", m.Version, from))
	}
	return strings.Join(out, " "), from
}

// --- helpers -----------------------------------------------------------------

func schemaSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// v062 makes p's empty database what v0.6.2's pg.Apply made (0001's schema,
// its hash in schema_version) and fills every table.
func v062(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	c := ctx(t)
	if _, err := p.Exec(c, pg.Migrations[0].SQL); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(c, "INSERT INTO schema_version (hash) VALUES ($1)", v062Hash); err != nil {
		t.Fatal(err)
	}
	fill(t, p)
}

// fill puts two rows in every table but schema_version, a value in every
// column, from the database's own catalog (so it fills whatever schema the
// database has).
func fill(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	c := ctx(t)
	r, err := p.Query(c, `SELECT c.relname::text, array_agg(a.attname::text ORDER BY a.attnum), array_agg(format_type(a.atttypid, a.atttypmod) ORDER BY a.attnum)
		FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind = 'r' AND c.relname <> 'schema_version'
		GROUP BY c.relname ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	type table struct {
		name        string
		cols, types []string
	}
	tables, err := pgx.CollectRows(r, func(row pgx.CollectableRow) (table, error) {
		var tb table
		return tb, row.Scan(&tb.name, &tb.cols, &tb.types)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		for i := 1; i <= 2; i++ {
			vals := make([]string, len(tb.cols))
			for j, typ := range tb.types {
				vals[j] = literal(t, typ, fmt.Sprintf("%s.%s.%d", tb.name, tb.cols[j], i), i)
			}
			sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tb.name, strings.Join(tb.cols, ", "), strings.Join(vals, ", "))
			if _, err := p.Exec(c, sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
	}
}

func literal(t *testing.T, typ, s string, i int) string {
	switch typ {
	case "text":
		return "'" + s + "'"
	case "bigint", "integer":
		return fmt.Sprint(i)
	case "boolean":
		return fmt.Sprint(i%2 == 0)
	case "double precision":
		return fmt.Sprintf("%d.5", i)
	case "jsonb":
		return fmt.Sprintf(`'{"v": "%s"}'`, s)
	case "text[]":
		return fmt.Sprintf("ARRAY['%s']", s)
	case "bigint[]":
		return fmt.Sprintf("ARRAY[%d]::bigint[]", i)
	case "timestamp with time zone":
		return fmt.Sprintf("'2026-11-14T10:00:0%dZ'", i)
	}
	t.Fatalf("no test value for a %s column", typ)
	return ""
}

// rows is every table's rows but schema_version's, each as JSON, sorted. With
// like (an earlier rows), only like's tables and the columns its rows have,
// so a column added since doesn't count.
func rows(t *testing.T, p *pgxpool.Pool, like map[string][]string) map[string][]string {
	t.Helper()
	c := ctx(t)
	var tables []string
	if err := p.QueryRow(c, `SELECT coalesce(array_agg(relname::text ORDER BY relname), '{}') FROM pg_class
		WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r' AND relname <> 'schema_version'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, tb := range tables {
		if like != nil {
			if _, ok := like[tb]; !ok {
				continue
			}
		}
		var keys []string
		if like != nil && len(like[tb]) > 0 {
			keys = jsonKeys(t, like[tb][0])
		}
		var rs []string
		if err := p.QueryRow(c, fmt.Sprintf(`SELECT coalesce(array_agg(r::text ORDER BY r::text), '{}') FROM (
			SELECT CASE WHEN $1::text[] IS NULL THEN to_jsonb(t)
				ELSE (SELECT jsonb_object_agg(k, v) FROM jsonb_each(to_jsonb(t)) e(k, v) WHERE k = ANY($1)) END AS r
			FROM %s t) x`, tb), keys).Scan(&rs); err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		if len(rs) > 0 {
			out[tb] = rs
		}
	}
	return out
}

func jsonKeys(t *testing.T, row string) []string {
	var m map[string]any
	if err := json.Unmarshal([]byte(row), &m); err != nil || len(m) == 0 {
		t.Fatalf("no columns in %s (%v)", row, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func sameRows(t *testing.T, want, got map[string][]string) {
	t.Helper()
	var bad []string
	for tb := range want {
		if strings.Join(want[tb], "\n") != strings.Join(got[tb], "\n") {
			bad = append(bad, fmt.Sprintf("%s: %d rows, want %d", tb, len(got[tb]), len(want[tb])))
		}
	}
	for tb := range got {
		if _, ok := want[tb]; !ok {
			bad = append(bad, fmt.Sprintf("%s: %d rows, want none", tb, len(got[tb])))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("rows differ:\n%s", strings.Join(bad, "\n"))
	}
}

// versions is schema_version's versions, in order.
func versions(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var s string
	if err := p.QueryRow(ctx(t), "SELECT string_agg(version::text, ' ' ORDER BY version) FROM schema_version").Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func versionList(n int) string {
	var out []string
	for v := 1; v <= n; v++ {
		out = append(out, fmt.Sprint(v))
	}
	return strings.Join(out, " ")
}

// catalog is the database's schema, one line each: its tables' columns in
// order (type, collation, NOT NULL, default, identity), its constraints and
// its indexes (without their generated names).
func catalog(t *testing.T, p *pgxpool.Pool) []string {
	t.Helper()
	var out []string
	for _, q := range []string{
		`SELECT 'column ' || c.relname || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
			|| coalesce(' COLLATE ' || co.collname, '') || CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
			|| coalesce(' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid), '')
			|| CASE a.attidentity WHEN 'a' THEN ' ALWAYS AS IDENTITY' WHEN 'd' THEN ' BY DEFAULT AS IDENTITY' ELSE '' END
		FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		JOIN pg_type ty ON ty.oid = a.atttypid
		LEFT JOIN pg_collation co ON co.oid = a.attcollation AND a.attcollation <> ty.typcollation
		LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
		WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind = 'r'
		ORDER BY c.relname, a.attnum`,
		`SELECT 'constraint ' || conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid)
		FROM pg_constraint WHERE connamespace = current_schema()::regnamespace ORDER BY 1`,
		`SELECT 'index ' || regexp_replace(pg_get_indexdef(indexrelid), '^CREATE (UNIQUE )?INDEX \S+ ', 'CREATE \1INDEX ')
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relnamespace = current_schema()::regnamespace ORDER BY 1`,
	} {
		r, err := p.Query(ctx(t), q)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(r, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, lines...)
	}
	if len(out) < 100 {
		t.Fatalf("a catalog of %d lines", len(out))
	}
	return out
}

func sameCatalog(t *testing.T, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	missing, extra := diff(want, got), diff(got, want)
	if len(missing) == 0 && len(extra) == 0 {
		t.Fatal("the same columns, in another order: schema.sql lists a migration's new columns last, as ALTER TABLE adds them")
	}
	t.Fatalf("the chain's schema isn't schema.sql's:\nonly in schema.sql:\n  %s\nonly from the chain:\n  %s",
		strings.Join(missing, "\n  "), strings.Join(extra, "\n  "))
}

func diff(a, b []string) []string {
	in := map[string]int{}
	for _, s := range b {
		in[s]++
	}
	var out []string
	for _, s := range a {
		if in[s] > 0 {
			in[s]--
		} else {
			out = append(out, s)
		}
	}
	return out
}
