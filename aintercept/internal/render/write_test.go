package render_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render/rendertest"
)

// Writing diffs into Postgres (unnest upserts and deletes), on a fresh
// database: every value round-trips, and a rewrite deletes only its own key's
// rows.

func applied(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	p, err := pg.Open(ctx, pgtest.Fresh(t), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := pg.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	return ctx, p
}

func write(t *testing.T, ctx context.Context, p *pgxpool.Pool, d *render.Diff) {
	t.Helper()
	if err := pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error { return d.Write(ctx, tx) }); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func read(t *testing.T, ctx context.Context, p *pgxpool.Pool, table, where string) [][]any {
	t.Helper()
	tb := pg.TableNamed(table)
	rows, err := p.Query(ctx, "SELECT * FROM "+table+" WHERE "+where+" ORDER BY 1, 2")
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([]any, error) {
		v, err := r.Values()
		return rendertest.FromPostgres(tb, v), err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// tricky is a row of the table with an awkward value in every column.
func tricky(tb *pg.Table, owner string, n int64) []any {
	row := make([]any, len(tb.Columns))
	for i, c := range tb.Columns {
		switch ty, _ := strings.CutSuffix(c.Type, " NOT NULL"); {
		case c.Name == render.OwnerColumn(tb.Name):
			row[i] = owner
		case ty == "text":
			row[i] = []string{`a"b\c{d},e`, "", "NULL", "é日\U0001F600", " x "}[(i+int(n))%5]
		case ty == "bigint":
			row[i] = []int64{n, 0, -1, math.MaxInt64, math.MinInt64}[(i+int(n))%5]
		case ty == "double precision":
			row[i] = []float64{0.1, math.Copysign(0, -1), math.Inf(1), 1e-300, math.MaxFloat64}[(i+int(n))%5]
		case ty == "boolean":
			row[i] = (i+int(n))%2 == 0
		case ty == "text[]":
			row[i] = [][]string{{}, {"", "NULL", `"q"`, `\`, "{,}"}, {"é"}}[(i+int(n))%3]
		case ty == "bigint[]":
			row[i] = [][]int64{{}, {math.MinInt64, 0, 7}}[(i+int(n))%2]
		}
		if (i+int(n))%7 == 3 && !isPK(tb, c.Name) && c.Name != render.OwnerColumn(tb.Name) && !strings.HasSuffix(c.Type, "[]") {
			row[i] = nil
		}
	}
	return row
}

func isPK(tb *pg.Table, name string) bool {
	for _, k := range tb.PK {
		if k == name {
			return true
		}
	}
	return false
}

func TestEveryValueRoundTrips(t *testing.T) {
	ctx, p := applied(t)
	for _, tb := range render.Tables {
		owner := "s1"
		if render.OwnerColumn(tb.Name) == "key" {
			owner = "s:s1"
		}
		other := owner
		if len(tb.PK) == 1 { // keyed by its owner alone (session_rows): two owners
			other += "b"
		}
		a, b := tricky(tb, owner, 1), tricky(tb, other, 2)
		write(t, ctx, p, &render.Diff{Key: "s:s1", Session: "s1", Tables: []render.TableDiff{{Table: tb, Upserts: [][]any{a, b}}}})
		got := read(t, ctx, p, tb.Name, "true")
		if len(got) != 2 {
			t.Fatalf("%s: %d rows back", tb.Name, len(got))
		}
		f, g := rendertest.New(), rendertest.New()
		f.Plant(tb.Name, a)
		f.Plant(tb.Name, b)
		for _, r := range got {
			g.Plant(tb.Name, r)
		}
		if d := g.Diff(f); d != "" {
			t.Fatalf("%s: %s", tb.Name, d)
		}
		// An upsert of a's key with b's values replaces it; a delete by key removes it.
		pk := make([]any, len(tb.PK))
		for j, k := range tb.PK {
			for i, c := range tb.Columns {
				if c.Name == k {
					pk[j] = a[i]
				}
			}
		}
		write(t, ctx, p, &render.Diff{Key: "s:s1", Session: "s1", Tables: []render.TableDiff{{Table: tb, Deletes: [][]any{pk}}}})
		if got := read(t, ctx, p, tb.Name, "true"); len(got) != 1 {
			t.Fatalf("%s: %d rows after the delete", tb.Name, len(got))
		}
	}
}

func TestRewriteDeletesOnlyItsKeysRows(t *testing.T) {
	ctx, p := applied(t)
	for _, owner := range []struct{ key, session string }{{"s:s1", "s1"}, {"s:s2", "s2"}, {"c:k:2026-11-04", ""}} {
		d := &render.Diff{Key: owner.key, Session: owner.session}
		for _, tb := range render.Tables {
			o := owner.session
			if render.OwnerColumn(tb.Name) == "key" {
				o = owner.key
			} else if o == "" {
				continue
			}
			d.Tables = append(d.Tables, render.TableDiff{Table: tb, Upserts: [][]any{tricky(tb, o, 1)}})
		}
		write(t, ctx, p, d)
	}
	count := func(table, owner string) int {
		return len(read(t, ctx, p, table, render.OwnerColumn(table)+" = '"+owner+"'"))
	}
	// s1 rewritten with nothing; c:'s rewritten with nothing.
	write(t, ctx, p, &render.Diff{Key: "s:s1", Session: "s1", Reset: true})
	write(t, ctx, p, &render.Diff{Key: "c:k:2026-11-04", Reset: true})
	for _, tb := range render.Tables {
		s1, s2 := "s1", "s2"
		if render.OwnerColumn(tb.Name) == "key" {
			s1, s2 = "s:s1", "s:s2"
			if n := count(tb.Name, "c:k:2026-11-04"); n != 0 {
				t.Errorf("%s: the c: key's rewrite left %d rows", tb.Name, n)
			}
		}
		if n := count(tb.Name, s1); n != 0 {
			t.Errorf("%s: s1's rewrite left %d rows", tb.Name, n)
		}
		if n := count(tb.Name, s2); n != 1 {
			t.Errorf("%s: s2 has %d rows after another key's rewrite", tb.Name, n)
		}
	}
}
