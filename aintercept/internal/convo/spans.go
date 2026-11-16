package convo

import "encoding/json"

// value is one JSON value in the body: its bytes and where they are. The body
// has been checked with json.Valid, so the walkers below only find the ends of
// values; they never copy the body.
type value struct {
	raw  []byte
	span Span
}

// kind is the value's first byte: '{', '[', '"', 'n' (null), 't', 'f', or a digit.
func (v value) kind() byte {
	if len(v.raw) == 0 {
		return 0
	}
	return v.raw[0]
}

// str is a string value's text.
func (v value) str() (string, bool) {
	var s string
	if v.kind() != '"' || json.Unmarshal(v.raw, &s) != nil {
		return "", false
	}
	return s, true
}

// root is the body's top-level value.
func root(body []byte) value {
	i, j := skipSpace(body, 0), len(body)
	for j > i && isSpace(body[j-1]) {
		j--
	}
	return value{raw: body[i:j], span: Span{i, j}}
}

// members is an object's members by key (the last of a repeated key, as
// encoding/json decodes it). ok is false when v isn't an object.
func members(v value) (map[string]value, bool) {
	if v.kind() != '{' {
		return nil, false
	}
	out := map[string]value{}
	b := v.raw
	i := skipSpace(b, 1)
	for i < len(b) && b[i] == '"' {
		kEnd := skipString(b, i)
		var key string
		if json.Unmarshal(b[i:kEnd], &key) != nil {
			return nil, false
		}
		i = skipSpace(b, kEnd)
		if i >= len(b) || b[i] != ':' {
			return nil, false
		}
		i = skipSpace(b, i+1)
		end := skipValue(b, i)
		out[key] = value{raw: b[i:end], span: Span{v.span.Start + i, v.span.Start + end}}
		i = skipSpace(b, end)
		if i < len(b) && b[i] == ',' {
			i = skipSpace(b, i+1)
		}
	}
	return out, true
}

// elements is an array's elements, in order. ok is false when v isn't an array.
func elements(v value) ([]value, bool) {
	if v.kind() != '[' {
		return nil, false
	}
	var out []value
	b := v.raw
	i := skipSpace(b, 1)
	for i < len(b) && b[i] != ']' {
		end := skipValue(b, i)
		if end <= i {
			return nil, false
		}
		out = append(out, value{raw: b[i:end], span: Span{v.span.Start + i, v.span.Start + end}})
		i = skipSpace(b, end)
		if i < len(b) && b[i] == ',' {
			i = skipSpace(b, i+1)
		}
	}
	return out, true
}

// skipValue is the end of the value that starts at b[i].
func skipValue(b []byte, i int) int {
	if i >= len(b) {
		return len(b)
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				i = skipString(b, i) - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
		}
		return len(b)
	}
	for i < len(b) && b[i] != ',' && b[i] != '}' && b[i] != ']' && !isSpace(b[i]) { // number, true, false, null
		i++
	}
	return i
}

// skipString is the end of the string that starts at b[i] (a quote).
func skipString(b []byte, i int) int {
	for i++; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(b)
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	return i
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
