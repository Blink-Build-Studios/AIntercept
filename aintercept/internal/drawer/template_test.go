package drawer

import (
	"math/rand"
	"strings"
	"testing"
)

// randomValue is a drawer-like JSON value: objects with label keys and others,
// lists, and leaves that are hard for a template (%, %s, %%, the separator's
// control character, quotes, non-ASCII, floats and big ints).
func randomValue(r *rand.Rand, depth int) Value {
	strs := []string{"", "x", "100%", "%s", "%%", "a%sb%%c", "\x1f", "\"q\"", "é日本", "tokens", "null", `"stepCount":null`}
	keys := []string{"k", "v", "unit", "tone", "note", "title", "accent", "key", "value", "label", "%", "rows", "body"}
	switch n := r.Intn(10); {
	case depth > 3 || n < 4:
		switch r.Intn(6) {
		case 0:
			return None
		case 1:
			return BoolV(r.Intn(2) == 0)
		case 2:
			return IntV(r.Int63n(1e12) - 5e11)
		case 3:
			return FloatV(r.NormFloat64() * 1e4)
		default:
			return StrV(strs[r.Intn(len(strs))] + strs[r.Intn(len(strs))])
		}
	case n < 7:
		l := Value{K: List, L: []Value{}}
		for i := r.Intn(4); i > 0; i-- {
			l.L = append(l.L, randomValue(r, depth+1))
		}
		return l
	default:
		o := Value{K: Object, O: []Member{}}
		for i := r.Intn(5); i > 0; i-- {
			o.O = append(o.O, Member{keys[r.Intn(len(keys))], randomValue(r, depth+1)})
		}
		return o
	}
}

// A row's template and values rebuild its compact JSON byte for byte (Format
// is Postgres's format() for %s and %%; the worker's tests run Postgres's own).
func TestTemplateRebuildsTheCompactJSON(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20_000; i++ {
		v := randomValue(r, 0)
		tpl, vals := Template(v)
		if got, want := Format(tpl, vals), compact(v); got != want {
			t.Fatalf("Format(Template(v)):\n %s\nwant\n %s\n(template %q, values %q)", got, want, tpl, vals)
		}
		if strings.Count(tpl, "%s") > 0 && vals == "" && strings.Count(tpl, "%s") != strings.Count(tpl, "%%s") {
			t.Fatalf("a template with values and none given: %q", tpl)
		}
	}
}

// Rows of one shape share their template: the labels and nulls are the shape,
// the rest are values.
func TestRowsOfOneShapeShareATemplate(t *testing.T) {
	row := func(n int64, tone string) Value {
		return obj("title", StrV("Tokens & cost"), "rows", list(obj("k", StrV("input"), "v", IntV(n), "unit", StrV("tokens"),
			"tone", StrV(tone), "note", None)))
	}
	t1, v1 := Template(row(5, "ok"))
	t2, v2 := Template(row(7, "ok"))
	t3, _ := Template(row(7, "muted"))
	if t1 != t2 || v1 == v2 || t1 == t3 {
		t.Fatalf("templates %q %q %q, values %q %q", t1, t2, t3, v1, v2)
	}
}
