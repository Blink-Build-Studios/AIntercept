package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"
)

// Metadata values as the API's Python and SQL read them, and the JSON helpers
// the facts read with.

// eventCols is the columns of an events part the rows read.
type eventCols struct {
	CorrelationID string
	RowNS         int64
	NS            int64
	Seq           int64
	Source        string
	Name          string
	SessionID     string
	PromptID      string
	PersonID      string
	MetadataJSON  string
	DeviceID      string
	EnvJSON       string
	HeadSHA       string
	OrgID         string
	EventKey      string
}

// RequestKind is a request's channel by its path, as the Requests tab's filter
// has it (kindOf). The v9 flush stores it at insert (docs/PIPELINE.md §3.1).
func RequestKind(path string) string { return kindOf(path) }

// kindOf is a request's channel, by its path (Codex's: model, analytics, metrics,
// usage, catalog; Claude Code's: model, count, analytics, flags, account).
func kindOf(path string) string {
	p, _, _ := strings.Cut(path, "?")
	switch {
	case p == "/backend-api/codex/responses":
		return "model"
	case strings.HasPrefix(p, "/backend-api/codex/analytics-events"):
		return "analytics"
	case strings.HasPrefix(p, "/otlp/"):
		return "metrics"
	case strings.HasPrefix(p, "/backend-api/wham/"), strings.HasPrefix(p, "/backend-api/accounts/"):
		return "usage"
	case strings.HasPrefix(p, "/backend-api/ps/"), strings.HasPrefix(p, "/backend-api/plugins/"),
		strings.HasPrefix(p, "/backend-api/codex/models"):
		return "catalog"
	case strings.HasPrefix(p, "/v1/messages/count_tokens"):
		return "count"
	case strings.HasPrefix(p, "/v1/messages"):
		return "model"
	case strings.HasPrefix(p, "/api/event_logging"):
		return "analytics"
	case strings.HasPrefix(p, "/api/eval/"):
		return "flags"
	}
	return "account"
}

// --- JSON values as DuckDB reads them -------------------------------------------------

// jsonFields is the named top-level fields of a JSON object, the first of a
// repeated key (as DuckDB's json_extract_string reads them).
func jsonFields(s string, keys ...string) map[string]json.RawMessage {
	if s == "" {
		return map[string]json.RawMessage{}
	}
	// One record's metadata is read by several facts in turn: the most recent
	// parse is reused. The map holds every top-level field (the first of a
	// repeated key); callers never change it.
	if c := lastFields.Load(); c != nil && c.s == s {
		return c.m
	}
	m := allFields(s)
	lastFields.Store(&parsedFields{s: s, m: m})
	return m
}

type parsedFields struct {
	s string
	m map[string]json.RawMessage
}

// lastFields is the most recently parsed object (shared by goroutines; each
// entry is immutable once stored).
var lastFields atomic.Pointer[parsedFields]

func allFields(s string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return out
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return out
		}
		k, _ := tok.(string)
		if _, seen := out[k]; !seen {
			out[k] = v
		}
	}
	return out
}

// jsonString is json_extract_string: a string's value, a scalar's text, an object's
// or array's JSON; false for null or absent.
func jsonString(v json.RawMessage) (string, bool) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || string(v) == "null" {
		return "", false
	}
	switch v[0] {
	case '"':
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s, true
		}
		return "", false
	case '{', '[':
		var b bytes.Buffer
		if json.Compact(&b, v) == nil {
			return b.String(), true
		}
	}
	return string(v), true
}

// jsonScalar is a scalar's json_extract_string, trimmed as a cast trims it;
// false for null, absent, an object or an array (no cast reads those).
func jsonScalar(v json.RawMessage) (string, bool) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || v[0] == '{' || v[0] == '[' {
		return "", false
	}
	s, ok := jsonString(v)
	return strings.TrimSpace(s), ok
}

// jsonInt is TRY_CAST(json_extract_string(…) AS INT/BIGINT): an integer in
// [lo, hi], a decimal rounded half away from zero; nil for anything else.
func jsonInt(v json.RawMessage, lo, hi int64) *int64 {
	s, ok := jsonScalar(v)
	if !ok {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < lo || n > hi {
			return nil
		}
		return &n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	f = math.Round(f)
	if f < float64(lo) || f > float64(hi) {
		return nil
	}
	n := int64(f)
	return &n
}

// jsonDouble is TRY_CAST(json_extract_string(…) AS DOUBLE).
func jsonDouble(v json.RawMessage) *float64 {
	s, ok := jsonScalar(v)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

// --- small helpers -------------------------------------------------------------------

// interner shares one copy of each repeated string (hosts, paths, sessions, models…).
type interner map[string]string

func (in interner) of(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := in[s]; ok {
		return v
	}
	v := strings.Clone(s)
	in[v] = v
	return v
}

func ptr[T any](v T) *T { return &v }

// pyStr is session.py _str: None for null, an object or a list; else str(v).
func pyStr(v json.RawMessage) *string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return nil
	}
	switch v[0] {
	case 'n', '{', '[':
		return nil
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil
		}
		return &s
	case 't':
		return ptr("True")
	case 'f':
		return ptr("False")
	}
	if isIntLiteral(v) {
		n, ok := new(big.Int).SetString(string(v), 10)
		if ok {
			return ptr(n.String())
		}
	}
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return nil
	}
	return ptr(pyRepr(f))
}

// pyTruthy is Python's bool(v) for a JSON value.
func pyTruthy(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	switch {
	case len(v) == 0, string(v) == "null", string(v) == "false", string(v) == `""`, string(v) == "[]", string(v) == "{}":
		return false
	case v[0] == '{' || v[0] == '[':
		var x any
		if json.Unmarshal(v, &x) == nil {
			switch t := x.(type) {
			case map[string]any:
				return len(t) > 0
			case []any:
				return len(t) > 0
			}
		}
		return true
	case v[0] == '"' || v[0] == 't':
		return true
	}
	f, err := strconv.ParseFloat(string(v), 64)
	return err != nil || f != 0
}

// pyIntOf is session.py _int: int(float(v)); None for a bool, null or anything
// float() refuses.
func pyIntOf(v json.RawMessage) *int64 {
	f := pyFloatOf(v)
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil
	}
	return truncInt(*f)
}

// pyFloatOf is session.py _float: float(v) for a number or a numeric string.
func pyFloatOf(v json.RawMessage) *float64 {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return nil
	}
	switch v[0] {
	case 'n', 't', 'f', '{', '[':
		return nil
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil
		}
		return pyParseFloat(s)
	}
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return nil
	}
	return &f
}

// pyTrue is session.py _true: v is True or v == "true".
func pyTrue(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	return string(v) == "true" || string(v) == `"true"`
}

// pyParseFloat is Python's float(str): surrounding whitespace, underscores between
// digits, inf and nan; no hex.
func pyParseFloat(s string) *float64 {
	s = strings.TrimSpace(s)
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
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && ne.Err == strconv.ErrRange {
			return &f // Python: overflow is inf, underflow 0
		}
		return nil
	}
	return &f
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isIntLiteral(v []byte) bool {
	for i, b := range v {
		if !(isDigit(b) || i == 0 && b == '-') {
			return false
		}
	}
	return true
}

// truncInt is int(f): toward zero.
func truncInt(f float64) *int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return ptr(int64(math.Trunc(f)))
}

// pyRepr is repr(float): the shortest round-trip digits, exponent form below 1e-4
// and from 1e16.
func pyRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
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
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
