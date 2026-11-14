package worker

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
)

// The drawer's statement rebuilds a row with Postgres's format(template,
// VARIADIC string_to_array(values, E'\x1f')): byte for byte the row's compact
// JSON, a template of hundreds of values included (§3.7).
func TestPostgresRebuildsADrawerTemplate(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	r := rand.New(rand.NewSource(1))
	var vs []drawer.Value
	for i := 0; i < 300; i++ {
		vs = append(vs, randomValue(r, 0))
	}
	big := drawer.Value{K: drawer.List}
	for i := 0; i < 400; i++ {
		big.L = append(big.L, drawer.IntV(int64(i)), drawer.StrV("50% %s"))
	}
	vs = append(vs, big, drawer.Value{K: drawer.Object})
	for _, v := range vs {
		tpl, vals := drawer.Template(v)
		var got string
		if err := conn.QueryRow(ctx, `SELECT format($1, VARIADIC string_to_array($2, E'\x1f'))`, tpl, vals).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := drawer.Dumps(v, ",", ":", -1); got != want {
			t.Fatalf("format(%q) = %q, want %q", tpl, got, want)
		}
	}
}

// randomValue is a drawer-like value with leaves that are hard for a template.
func randomValue(r *rand.Rand, depth int) drawer.Value {
	strs := []string{"", "x", "100%", "%s", "%%", "a%sb%%c", "\x1f", `"q"`, "é日本", "tokens", "null"}
	keys := []string{"k", "v", "unit", "tone", "note", "title", "key", "value", "%", "rows"}
	switch n := r.Intn(10); {
	case depth > 3 || n < 4:
		switch r.Intn(5) {
		case 0:
			return drawer.None
		case 1:
			return drawer.BoolV(r.Intn(2) == 0)
		case 2:
			return drawer.IntV(r.Int63n(1e12) - 5e11)
		case 3:
			return drawer.FloatV(r.NormFloat64() * 1e4)
		default:
			return drawer.StrV(strings.Repeat(strs[r.Intn(len(strs))], 1+r.Intn(2)))
		}
	case n < 7:
		l := drawer.Value{K: drawer.List, L: []drawer.Value{}}
		for i := r.Intn(4); i > 0; i-- {
			l.L = append(l.L, randomValue(r, depth+1))
		}
		return l
	default:
		o := drawer.Value{K: drawer.Object, O: []drawer.Member{}}
		for i := r.Intn(5); i > 0; i-- {
			o.O = append(o.O, drawer.Member{Key: keys[r.Intn(len(keys))], Val: randomValue(r, depth+1)})
		}
		return o
	}
}
