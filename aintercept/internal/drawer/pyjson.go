package drawer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Value is a JSON value as Python's json.loads gives it: an int is Python's
// (any size, its digits), a float its float, an object a dict (its keys in
// order; a repeated key keeps its first place and its last value).
type Value struct {
	K Kind
	B bool
	N string  // Int: its digits
	F float64 // Float
	S string  // Str
	L []Value
	O []Member
}

// Member is one key of an object.
type Member struct {
	Key string
	Val Value
}

// Kind is a Value's Python type.
type Kind uint8

const (
	Null Kind = iota
	Bool
	Int
	Float
	Str
	List
	Object
)

// None is Python's None.
var None = Value{}

// StrV is a str.
func StrV(s string) Value { return Value{K: Str, S: s} }

// IntV is an int.
func IntV(n int64) Value { return Value{K: Int, N: strconv.FormatInt(n, 10)} }

// FloatV is a float.
func FloatV(f float64) Value { return Value{K: Float, F: f} }

// BoolV is a bool.
func BoolV(b bool) Value { return Value{K: Bool, B: b} }

// Loads is json.loads(text); ok false where it raises.
func Loads(text []byte) (Value, bool) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return None, false
	}
	if _, err := dec.Token(); err == nil { // json.loads: "Extra data"
		return None, false
	}
	return v, true
}

func decodeValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return None, err
	}
	switch t := tok.(type) {
	case nil:
		return None, nil
	case bool:
		return BoolV(t), nil
	case string:
		return StrV(t), nil
	case json.Number:
		return number(string(t)), nil
	case json.Delim:
		switch t {
		case '[':
			l := []Value{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return None, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return None, err
			}
			return Value{K: List, L: l}, nil
		case '{':
			o := []Member{}
			at := map[string]int{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return None, err
				}
				k, _ := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return None, err
				}
				if i, ok := at[k]; ok {
					o[i].Val = v
					continue
				}
				at[k] = len(o)
				o = append(o, Member{k, v})
			}
			if _, err := dec.Token(); err != nil {
				return None, err
			}
			return Value{K: Object, O: o}, nil
		}
	}
	return None, fmt.Errorf("drawer: unexpected token %v", tok)
}

// number is a JSON number as Python reads it: an int without a fraction or
// exponent, else a float (inf past the float range).
func number(s string) Value {
	if !strings.ContainsAny(s, ".eE") {
		n, ok := new(big.Int).SetString(s, 10)
		if ok {
			return Value{K: Int, N: n.String()}
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return FloatV(f)
}

// Get is d.get(key): None when v isn't a dict or has no such key.
func (v Value) Get(key string) Value {
	if v.K != Object {
		return None
	}
	for _, m := range v.O {
		if m.Key == key {
			return m.Val
		}
	}
	return None
}

// Has is `key in d`.
func (v Value) Has(key string) bool {
	if v.K != Object {
		return false
	}
	for _, m := range v.O {
		if m.Key == key {
			return true
		}
	}
	return false
}

// IsNone is `v is None`.
func (v Value) IsNone() bool { return v.K == Null }

// Truthy is bool(v).
func (v Value) Truthy() bool {
	switch v.K {
	case Null:
		return false
	case Bool:
		return v.B
	case Int:
		return v.N != "0"
	case Float:
		return v.F != 0
	case Str:
		return v.S != ""
	case List:
		return len(v.L) > 0
	}
	return len(v.O) > 0
}

// Or is `v or w`.
func (v Value) Or(w Value) Value {
	if v.Truthy() {
		return v
	}
	return w
}

// IsInt is isinstance(v, int) (a bool is one).
func (v Value) IsInt() bool { return v.K == Int || v.K == Bool }

// Int64 is an int's value (a bool's 0 or 1); ok false for anything else, or
// one past int64.
func (v Value) Int64() (int64, bool) {
	switch v.K {
	case Bool:
		if v.B {
			return 1, true
		}
		return 0, true
	case Int:
		n, err := strconv.ParseInt(v.N, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// Len is len(v) for a str, list or dict.
func (v Value) Len() int {
	switch v.K {
	case Str:
		return utf8.RuneCountInString(v.S)
	case List:
		return len(v.L)
	case Object:
		return len(v.O)
	}
	return 0
}

// Eq is `v == w` for the values the drawer compares (None, str, numbers).
func (v Value) Eq(w Value) bool {
	switch {
	case v.K == Null || w.K == Null:
		return v.K == w.K
	case v.K == Str || w.K == Str:
		return v.K == w.K && v.S == w.S
	}
	a, aok := v.float()
	b, bok := w.float()
	if v.K == Int && w.K == Int {
		return v.N == w.N
	}
	return aok && bok && a == b
}

func (v Value) float() (float64, bool) {
	switch v.K {
	case Bool:
		if v.B {
			return 1, true
		}
		return 0, true
	case Int:
		f, _ := strconv.ParseFloat(v.N, 64)
		return f, true
	case Float:
		return v.F, true
	}
	return 0, false
}

// --- Python's str(), repr() and float() ---------------------------------------

// Str is str(v).
func (v Value) Str() string {
	switch v.K {
	case Str:
		return v.S
	case Null:
		return "None"
	case Bool:
		if v.B {
			return "True"
		}
		return "False"
	case Int:
		return v.N
	case Float:
		return pyRepr(v.F)
	}
	return v.Repr()
}

// Repr is repr(v).
func (v Value) Repr() string {
	switch v.K {
	case Str:
		return reprStr(v.S)
	case List:
		parts := make([]string, len(v.L))
		for i, x := range v.L {
			parts[i] = x.Repr()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Object:
		parts := make([]string, len(v.O))
		for i, m := range v.O {
			parts[i] = reprStr(m.Key) + ": " + m.Val.Repr()
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return v.Str()
}

// reprStr is repr(str): single quotes unless it holds one and no double quote.
func reprStr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case !isPrintable(r):
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// isPrintable is str.isprintable for one character: not a separator or other
// (Z*, C*) but the space.
func isPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	return !unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs) &&
		unicode.IsGraphic(r)
}

// pyRepr is repr(float): the shortest round-trip digits, exponent form below
// 1e-4 and from 1e16.
func pyRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exps, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(exps)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		return mant + "e" + sign + fmt.Sprintf("%02d", exp)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// pyFloat is session.py _float: float(v); nil for None, a bool, or what
// float() refuses.
func pyFloat(v Value) *float64 {
	switch v.K {
	case Int:
		f, _ := strconv.ParseFloat(v.N, 64)
		return &f
	case Float:
		f := v.F
		return &f
	case Str:
		return parseFloatStr(v.S)
	}
	return nil
}

// pyInt is session.py _int: int(float(v)).
func pyInt(v Value) *int64 {
	f := pyFloat(v)
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil
	}
	n := int64(math.Trunc(*f))
	return &n
}

// pyStr is session.py _str: None for None, a dict or a list; else str(v).
func pyStr(v Value) *string {
	switch v.K {
	case Null, List, Object:
		return nil
	}
	s := v.Str()
	return &s
}

// parseFloatStr is float(str): surrounding whitespace, underscores between
// digits, inf and nan; no hex.
func parseFloatStr(s string) *float64 {
	s = strings.TrimFunc(s, isPySpace)
	if s == "" || strings.ContainsAny(s, "xXpP") {
		return nil
	}
	if strings.Contains(s, "_") {
		for i := range len(s) {
			if s[i] == '_' && (i == 0 || i == len(s)-1 || !isDigit(s[i-1]) || !isDigit(s[i+1])) {
				return nil
			}
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	switch strings.ToLower(strings.TrimLeft(s, "+-")) {
	case "inf", "infinity", "nan":
	default:
		for i := range len(s) {
			c := s[i]
			if !(isDigit(c) || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-') {
				return nil
			}
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return &f
		}
		return nil
	}
	return &f
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// isPySpace is str.isspace for one character (what str.strip() strips).
func isPySpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// --- json.dumps -------------------------------------------------------------------

// Dumps is json.dumps(v) with ensure_ascii, the separators given, and indent
// (< 0: none).
func Dumps(v Value, itemSep, keySep string, indent int) string {
	var b strings.Builder
	dumps(&b, v, itemSep, keySep, indent, 0)
	return b.String()
}

func dumps(b *strings.Builder, v Value, itemSep, keySep string, indent, depth int) {
	nl := func(d int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*d))
		}
	}
	sep := itemSep
	if indent >= 0 {
		sep = strings.TrimRight(itemSep, " ")
	}
	switch v.K {
	case Null:
		b.WriteString("null")
	case Bool:
		if v.B {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Int:
		b.WriteString(v.N)
	case Float:
		switch {
		case math.IsNaN(v.F):
			b.WriteString("NaN")
		case math.IsInf(v.F, 1):
			b.WriteString("Infinity")
		case math.IsInf(v.F, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(pyRepr(v.F))
		}
	case Str:
		b.WriteString(asciiQuote(v.S))
	case List:
		if len(v.L) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, x := range v.L {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(depth + 1)
			dumps(b, x, itemSep, keySep, indent, depth+1)
		}
		nl(depth)
		b.WriteByte(']')
	case Object:
		if len(v.O) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, m := range v.O {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(depth + 1)
			b.WriteString(asciiQuote(m.Key))
			b.WriteString(keySep)
			dumps(b, m.Val, itemSep, keySep, indent, depth+1)
		}
		nl(depth)
		b.WriteByte('}')
	}
}

// asciiQuote is a str as json.dumps writes it with ensure_ascii.
func asciiQuote(s string) string {
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		return `"` + s + `"`
	}
	const hex = "0123456789abcdef"
	b := make([]byte, 0, len(s)+16)
	b = append(b, '"')
	u := func(r rune) {
		b = append(b, '\\', 'u', hex[r>>12&0xf], hex[r>>8&0xf], hex[r>>4&0xf], hex[r&0xf])
	}
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		default:
			switch {
			case r < 0x20 || r > 0x7e && r < 0x10000:
				u(r)
			case r >= 0x10000:
				r -= 0x10000
				u(0xd800 + (r >> 10))
				u(0xdc00 + (r & 0x3ff))
			default:
				b = append(b, byte(r))
			}
		}
	}
	return string(append(b, '"'))
}

// sortedMembers is a dict's keys in sorted order (code points: UTF-8 bytes).
func sortedMembers(o []Member) []Member {
	out := append([]Member(nil), o...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
