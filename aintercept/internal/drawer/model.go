package drawer

import (
	"strconv"
	"strings"
	"time"
)

// The drawer's response objects (api models/requests.py), built as Values and
// written as JSON the API validates through today's models: the JSON's order
// doesn't matter, its values do (an int stays an int, a float a float, by
// Python's repr).

// ContentRow is models.ContentRow.
type ContentRow struct {
	Label  string
	Detail string
	Size   *int64
	Unit   *string
	Tone   *string
	Body   *string
}

// DetailRow is models.DetailRow: V is the value as Python had it (str, int,
// float, bool or None).
type DetailRow struct {
	K    string
	V    Value
	Unit *string
	Tone *string
	Note *string
}

func (c ContentRow) value() Value {
	return obj("label", StrV(c.Label), "detail", StrV(c.Detail), "size", vInt(c.Size), "unit", vStr(c.Unit),
		"tone", vStr(c.Tone), "body", vStr(c.Body))
}

func (r DetailRow) value() Value {
	return obj("k", StrV(r.K), "v", r.V, "unit", vStr(r.Unit), "tone", vStr(r.Tone), "note", vStr(r.Note))
}

func contentRows(rows []ContentRow) Value {
	out := Value{K: List, L: []Value{}}
	for _, r := range rows {
		out.L = append(out.L, r.value())
	}
	return out
}

func detailRows(rows []DetailRow) Value {
	out := Value{K: List, L: []Value{}}
	for _, r := range rows {
		out.L = append(out.L, r.value())
	}
	return out
}

func row(k string, v Value, unit string) DetailRow {
	r := DetailRow{K: k, V: v}
	if unit != "" {
		r.Unit = sp(unit)
	}
	return r
}

func (r DetailRow) tone(t string) DetailRow {
	if t != "" {
		r.Tone = sp(t)
	}
	return r
}

func (r DetailRow) note(n *string) DetailRow {
	r.Note = n
	return r
}

// obj is a dict of alternating keys and values.
func obj(kv ...any) Value {
	out := Value{K: Object, O: make([]Member, 0, len(kv)/2)}
	for i := 0; i < len(kv); i += 2 {
		out.O = append(out.O, Member{kv[i].(string), kv[i+1].(Value)})
	}
	return out
}

func list(vs ...Value) Value { return Value{K: List, L: append([]Value{}, vs...)} }

func vStr(s *string) Value {
	if s == nil {
		return None
	}
	return StrV(*s)
}

func vInt(n *int64) Value {
	if n == nil {
		return None
	}
	return IntV(*n)
}

func vFloat(f *float64) Value {
	if f == nil {
		return None
	}
	return FloatV(*f)
}

func sp(s string) *string { return &s }
func i64(n int64) *int64  { return &n }
func f64(f float64) *float64 {
	return &f
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// round1 is round(x, 1).
func round1(x float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	return f
}

// msBetween is request_detail._ms: round((b - a) / 1e6, 1); nil when either is None.
func msBetween(a, b Value) *float64 {
	x, ok1 := a.Int64()
	y, ok2 := b.Int64()
	if a.IsNone() || b.IsNone() || !ok1 || !ok2 {
		return nil
	}
	return f64(round1(float64(y-x) / 1e6))
}

// secs is request_detail._secs.
func secs(ms float64) string {
	s := ms / 1000
	if s < 60 {
		return strconv.FormatFloat(s, 'f', 1, 64) + " s"
	}
	m := pyFloorDiv(s, 60)
	rest := pyMod(s, 60)
	r := int64(roundHalfEven(rest))
	return strconv.FormatInt(int64(m), 10) + "m " + pad2(r) + "s"
}

func pad2(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n >= 0 && len(s) < 2 {
		return "0" + s
	}
	return s
}

// pyFloorDiv is Python's float //: floor((a - a % b) / b).
func pyFloorDiv(a, b float64) float64 { return floorOf((a - pyMod(a, b)) / b) }

func floorOf(x float64) float64 {
	f := float64(int64(x))
	if f > x {
		f--
	}
	return f
}

// pyMod is Python's float %: the sign of the divisor.
func pyMod(a, b float64) float64 {
	m := a - b*floorOf(a/b)
	if m != 0 && (m < 0) != (b < 0) {
		m += b
	}
	return m
}

func roundHalfEven(x float64) float64 {
	t := floorOf(x)
	d := x - t
	switch {
	case d > 0.5:
		return t + 1
	case d < 0.5:
		return t
	}
	if int64(t)%2 == 0 {
		return t
	}
	return t + 1
}

// dt is rows.dt: the time, truncated to microseconds, as an ISO string the API
// reads back as a UTC datetime.
func dt(ns int64) Value {
	us := ns / 1000
	if ns%1000 != 0 && ns < 0 {
		us--
	}
	return StrV(time.UnixMicro(us).UTC().Format("2006-01-02T15:04:05.000000Z"))
}

// commaInt is f"{n:,}".
func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// formatG is f"{x:g}".
func formatG(x float64) string {
	switch s := strconv.FormatFloat(x, 'g', 6, 64); s {
	case "+Inf":
		return "inf"
	case "-Inf":
		return "-inf"
	case "NaN":
		return "nan"
	default:
		return s
	}
}
