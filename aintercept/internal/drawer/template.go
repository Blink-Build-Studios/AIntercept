package drawer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// A drawer row's JSON as a template and its values (docs/PIPELINE.md §3.7,
// v8): the template is the JSON's shape (its keys, its labels and its nulls)
// with each other value a %s, and the values are those values' JSON. Rows of
// one shape share a template, kept once (drawer_templates, by its hash); the
// drawer's statement rebuilds the row with Postgres's format(template,
// VARIADIC values). format(Template(v)) is Compact(v), byte for byte.

// labelKeys are the keys whose str values are labels (a section's title and
// accent, a detail row's key, unit and tone, a field's key): kept in the
// template.
var labelKeys = map[string]bool{"title": true, "accent": true, "k": true, "unit": true, "tone": true, "key": true}

// ValueSep joins a row's values: JSON text never holds a raw control character.
const ValueSep = "\x1f"

// Template is v's compact JSON as a template and its values, joined by ValueSep.
func Template(v Value) (tpl, vals string) {
	var t, x strings.Builder
	n := 0
	template(&t, &x, &n, v, "")
	return t.String(), x.String()
}

// TemplateID is a template's id: the hex of its sha256's first 16 bytes.
func TemplateID(tpl string) string {
	h := sha256.Sum256([]byte(tpl))
	return hex.EncodeToString(h[:16])
}

func escapePct(s string) string { return strings.ReplaceAll(s, "%", "%%") }

func template(t, x *strings.Builder, n *int, v Value, parent string) {
	switch v.K {
	case List:
		if len(v.L) == 0 {
			t.WriteString("[]")
			return
		}
		t.WriteByte('[')
		for i, e := range v.L {
			if i > 0 {
				t.WriteByte(',')
			}
			template(t, x, n, e, parent)
		}
		t.WriteByte(']')
	case Object:
		if len(v.O) == 0 {
			t.WriteString("{}")
			return
		}
		t.WriteByte('{')
		for i, m := range v.O {
			if i > 0 {
				t.WriteByte(',')
			}
			t.WriteString(escapePct(asciiQuote(m.Key)))
			t.WriteByte(':')
			template(t, x, n, m.Val, m.Key)
		}
		t.WriteByte('}')
	case Null:
		t.WriteString("null")
	default:
		lit := Dumps(v, ",", ":", -1)
		if v.K == Str && labelKeys[parent] {
			t.WriteString(escapePct(lit))
			return
		}
		t.WriteString("%s")
		if *n > 0 {
			x.WriteString(ValueSep)
		}
		x.WriteString(lit)
		*n++
	}
}

// request is a row of id: data and details as templates (interned in m) and values.
func (m *Memo) request(id string, data, details Value) Request {
	r := Request{ID: id}
	var dt, dtt string
	dt, r.DataVals = Template(data)
	dtt, r.DetailsVals = Template(details)
	r.DataTpl, r.DetailsTpl = m.intern(dt), m.intern(dtt)
	return r
}

func (m *Memo) intern(tpl string) string {
	id := TemplateID(tpl)
	if _, ok := m.tpls[id]; !ok {
		m.tpls[id] = tpl
	}
	return id
}

// Template is the template of id, as the render that made the memo holds it.
func (m *Memo) Template(id string) string { return m.tpls[id] }

// Unwritten is the templates (id, body) of ids that the key's commits haven't
// written since the memo was made whole, which it then counts as written: a
// commit inserts them before the rows that reference them.
func (m *Memo) Unwritten(ids []string) [][2]string {
	var out [][2]string
	for _, id := range ids {
		if !m.written[id] {
			m.written[id] = true
			out = append(out, [2]string{id, m.tpls[id]})
		}
	}
	return out
}

// Format is Postgres's format(tpl, VARIADIC string_to_array(vals, ValueSep))
// for a template's %s and %%: the row's JSON, as the drawer's statement
// rebuilds it.
func Format(tpl, vals string) string {
	var parts []string
	if vals != "" {
		parts = strings.Split(vals, ValueSep)
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(tpl); i++ {
		if tpl[i] != '%' || i+1 >= len(tpl) {
			b.WriteByte(tpl[i])
			continue
		}
		i++
		if tpl[i] == '%' {
			b.WriteByte('%')
			continue
		}
		b.WriteString(parts[n]) // %s
		n++
	}
	return b.String()
}
