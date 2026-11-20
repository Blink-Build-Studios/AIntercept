package capture

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// The anonymizer rewrites JSON in place: every byte stays as sent (key order,
// spacing, number formatting) except the strings it replaces, so a case
// reads like the traffic it came from and the extractors see the same
// structure.

// node is where a value sits: its key (or its parent's, for an array element),
// the keys beside it, and the keys above it.
type node struct {
	key      string
	siblings map[string]string // the enclosing object's keys, with their string values ("" for others)
	parent   *node
}

func (n *node) under(key string) bool {
	for p := n; p != nil; p = p.parent {
		if p.key == key {
			return true
		}
	}
	return false
}

var errJSON = errors.New("not JSON")

// rewriteJSON returns b with each string value replaced by str(n, value) and
// each object key by key(value).
func rewriteJSON(b []byte, str func(n *node, s string) string, key func(s string) string) ([]byte, error) {
	w := &jw{in: b, str: str, key: key}
	w.ws()
	if err := w.value(&node{}); err != nil {
		return nil, err
	}
	w.ws()
	if w.i != len(w.in) {
		return nil, errJSON
	}
	return w.out, nil
}

type jw struct {
	in       []byte
	i        int
	out      []byte
	str      func(*node, string) string
	key      func(string) string
	scanOnly bool
}

func (w *jw) ws() {
	for w.i < len(w.in) {
		switch w.in[w.i] {
		case ' ', '\t', '\n', '\r':
			if !w.scanOnly {
				w.out = append(w.out, w.in[w.i])
			}
			w.i++
		default:
			return
		}
	}
}

func (w *jw) emit(b []byte) {
	if !w.scanOnly {
		w.out = append(w.out, b...)
	}
}

func (w *jw) value(n *node) error {
	if w.i >= len(w.in) {
		return errJSON
	}
	switch c := w.in[w.i]; {
	case c == '{':
		return w.object(n)
	case c == '[':
		return w.array(n)
	case c == '"':
		s, raw, err := w.stringToken()
		if err != nil {
			return err
		}
		if w.scanOnly {
			return nil
		}
		if r := w.str(n, s); r != s {
			w.out = append(w.out, quote(r)...)
		} else {
			w.out = append(w.out, raw...)
		}
		return nil
	default:
		start := w.i
		for w.i < len(w.in) && !strings.ContainsRune(",]} \t\n\r", rune(w.in[w.i])) {
			w.i++
		}
		if w.i == start {
			return errJSON
		}
		w.emit(w.in[start:w.i])
		return nil
	}
}

// siblings scans the object starting at w.i for its keys and their string values.
func (w *jw) siblings() map[string]string {
	sc := &jw{in: w.in, i: w.i + 1, scanOnly: true}
	out := map[string]string{}
	for {
		sc.ws()
		if sc.i >= len(sc.in) || sc.in[sc.i] == '}' {
			return out
		}
		k, _, err := sc.stringToken()
		if err != nil {
			return out
		}
		sc.ws()
		if sc.i >= len(sc.in) || sc.in[sc.i] != ':' {
			return out
		}
		sc.i++
		sc.ws()
		v := ""
		if sc.i < len(sc.in) && sc.in[sc.i] == '"' {
			v, _, err = sc.stringToken()
		} else {
			err = sc.value(&node{})
		}
		if err != nil {
			return out
		}
		out[k] = v
		sc.ws()
		if sc.i < len(sc.in) && sc.in[sc.i] == ',' {
			sc.i++
		}
	}
}

func (w *jw) object(n *node) error {
	var sib map[string]string
	if !w.scanOnly {
		sib = w.siblings()
	}
	w.emit([]byte{'{'})
	w.i++
	w.ws()
	if w.i < len(w.in) && w.in[w.i] == '}' {
		w.emit([]byte{'}'})
		w.i++
		return nil
	}
	for {
		w.ws()
		k, raw, err := w.stringToken()
		if err != nil {
			return err
		}
		if !w.scanOnly {
			if r := w.key(k); r != k {
				w.out = append(w.out, quote(r)...)
			} else {
				w.out = append(w.out, raw...)
			}
		}
		w.ws()
		if w.i >= len(w.in) || w.in[w.i] != ':' {
			return errJSON
		}
		w.emit([]byte{':'})
		w.i++
		w.ws()
		if err := w.value(&node{key: k, siblings: sib, parent: n}); err != nil {
			return err
		}
		w.ws()
		if w.i >= len(w.in) {
			return errJSON
		}
		switch w.in[w.i] {
		case ',':
			w.emit([]byte{','})
			w.i++
		case '}':
			w.emit([]byte{'}'})
			w.i++
			return nil
		default:
			return errJSON
		}
	}
}

func (w *jw) array(n *node) error {
	w.emit([]byte{'['})
	w.i++
	w.ws()
	if w.i < len(w.in) && w.in[w.i] == ']' {
		w.emit([]byte{']'})
		w.i++
		return nil
	}
	for {
		w.ws()
		// An element takes its array's key, so its strings are judged as the array's.
		if err := w.value(&node{key: n.key, siblings: n.siblings, parent: n.parent}); err != nil {
			return err
		}
		w.ws()
		if w.i >= len(w.in) {
			return errJSON
		}
		switch w.in[w.i] {
		case ',':
			w.emit([]byte{','})
			w.i++
		case ']':
			w.emit([]byte{']'})
			w.i++
			return nil
		default:
			return errJSON
		}
	}
}

// stringToken reads a JSON string at w.i: its value and its bytes as sent.
func (w *jw) stringToken() (string, []byte, error) {
	if w.i >= len(w.in) || w.in[w.i] != '"' {
		return "", nil, errJSON
	}
	start := w.i
	w.i++
	esc := false
	for w.i < len(w.in) {
		c := w.in[w.i]
		w.i++
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			raw := w.in[start:w.i]
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return "", nil, errJSON
			}
			return s, raw, nil
		}
	}
	return "", nil, errJSON
}

// quote is s as a JSON string, escaping only what JSON requires (no HTML
// escapes), so the bytes stay close to what clients send.
func quote(s string) []byte {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			out = append(out, '\\', '"')
		case r == '\\':
			out = append(out, '\\', '\\')
		case r == '\n':
			out = append(out, '\\', 'n')
		case r == '\r':
			out = append(out, '\\', 'r')
		case r == '\t':
			out = append(out, '\\', 't')
		case r < 0x20:
			out = append(out, []byte(`\u00`)...)
			out = append(out, "0123456789abcdef"[r>>4], "0123456789abcdef"[r&0xf])
		case r == utf8.RuneError && size == 1:
			out = append(out, []byte(`�`)...)
		default:
			out = append(out, s[i:i+size]...)
		}
		i += size
	}
	return append(out, '"')
}
