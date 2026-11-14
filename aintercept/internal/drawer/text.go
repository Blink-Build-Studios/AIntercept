package drawer

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Python's text semantics, where the drawer's output depends on them.

// decodeReplace is bytes.decode("utf-8", errors="replace"): each maximal
// subpart of an ill-formed sequence becomes one U+FFFD (CPython's decoder).
func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := 0, byte(0x80), byte(0xbf)
		switch {
		case c >= 0xc2 && c <= 0xdf:
			need = 1
		case c == 0xe0:
			need, lo = 2, 0xa0
		case c >= 0xe1 && c <= 0xec, c == 0xee, c == 0xef:
			need = 2
		case c == 0xed:
			need, hi = 2, 0x9f
		case c == 0xf0:
			need, lo = 3, 0x90
		case c >= 0xf1 && c <= 0xf3:
			need = 3
		case c == 0xf4:
			need, hi = 3, 0x8f
		default:
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := 1
		for ; j <= need; j++ {
			if i+j >= len(b) {
				break
			}
			x := b[i+j]
			l, h := byte(0x80), byte(0xbf)
			if j == 1 {
				l, h = lo, hi
			}
			if x < l || x > h {
				break
			}
		}
		if j > need {
			out.Write(b[i : i+need+1])
			i += need + 1
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += j
	}
	return out.String()
}

// splitLines is str.splitlines().
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			i += n
			start = i
			continue
		case '\r':
			out = append(out, s[start:i])
			i += n
			if i < len(s) && s[i] == '\n' {
				i++
			}
			start = i
			continue
		}
		i += n
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func strip(s string) string { return strings.TrimFunc(s, isPySpace) }

// runes is len(s) in Python: code points.
func runes(s string) int { return utf8.RuneCountInString(s) }

// prefixRunes is s[:n] in Python.
func prefixRunes(s string, n int) (string, string) {
	i := 0
	for k := 0; k < n && i < len(s); k++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i], s[i:]
}

// --- redaction (request_detail.SECRET_TEXT) --------------------------------------

// pySpace is Python's \s in a str pattern.
const pySpace = `[\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`

// The alternatives of SECRET_TEXT, in its order; bounded marks those starting
// with \b (each begins with a word character, so \b there means the character
// before isn't one).
var secretAlts = []string{
	`sk-ant-[A-Za-z0-9_\-]{8,}`,
	`Bearer` + pySpace + `+[A-Za-z0-9._~+/\-]{12,}=*`,
	`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`,
	`gh[pousr]_[A-Za-z0-9]{20,}`,
	`xox[abpr]-[A-Za-z0-9\-]{10,}`,
	`ya29\.[A-Za-z0-9_\-]{20,}`,
	`"(?:access_token|refresh_token|id_token|client_secret)"` + pySpace + `*:` + pySpace + `*"[^"]+"`,
}

var (
	bounded  = []bool{false, true, true, true, true, true, false}
	secretAt = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(secretAlts))
		for i, a := range secretAlts {
			out[i] = regexp.MustCompile(`^(?:` + a + `)`)
		}
		return out
	}()
)

// secretStarts are the literals the alternatives start with, each with its
// alternative: a match can only start where one of them does. No two start
// with the same byte, but for the last alternative's four keys, which differ
// in their second: so at most one alternative can match at a position.
var secretStarts = []struct {
	lit string
	alt int
}{
	{"sk-ant-", 0}, {"Bearer", 1}, {"eyJ", 2}, {"gh", 3}, {"xox", 4}, {"ya29.", 5},
	{`"access_token"`, 6}, {`"refresh_token"`, 6}, {`"id_token"`, 6}, {`"client_secret"`, 6},
}

// isWord is Python's \w for a str: alphanumeric (a letter or a number) or _.
func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// redact is request_detail.redact: SECRET_TEXT.sub("[redacted]", text), with
// Python's leftmost-first matching, \b and \s. Python tries the alternatives
// in order at each position, leftmost first; only the one whose literal starts
// there can match, so the leftmost match is the first literal occurrence whose
// alternative matches there (with its \b). The literals are found with
// strings.Index, so a text with none costs a few memchr-speed scans, never a
// regular expression over the whole body.
func redact(s string) string {
	var next [10]int // each literal's next occurrence at or after pos; -1: none left
	for i, st := range secretStarts {
		next[i] = strings.Index(s, st.lit)
	}
	var b strings.Builder
	last, pos := 0, 0
	for {
		at := -1
		for i := range next {
			if next[i] >= 0 && (at < 0 || next[i] < next[at]) {
				at = i
			}
		}
		if at < 0 {
			break
		}
		q, alt := next[at], secretStarts[at].alt
		end := -1
		if r, _ := utf8.DecodeLastRuneInString(s[:q]); !bounded[alt] || q == 0 || !isWord(r) {
			if m := secretAt[alt].FindStringIndex(s[q:]); m != nil {
				end = q + m[1]
			}
		}
		if end < 0 {
			pos = q + 1 // the literal starts with an ASCII byte: the next character
		} else {
			if b.Len() == 0 {
				b.Grow(len(s))
			}
			b.WriteString(s[last:q])
			b.WriteString("[redacted]")
			last, pos = end, end
		}
		for i, st := range secretStarts {
			if next[i] >= 0 && next[i] < pos {
				if k := strings.Index(s[pos:], st.lit); k >= 0 {
					next[i] = pos + k
				} else {
					next[i] = -1
				}
			}
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// scrub is request_detail._scrub: every str in a value redacted.
func scrub(v Value) Value {
	switch v.K {
	case Str:
		return StrV(redact(v.S))
	case List:
		out := make([]Value, len(v.L))
		for i, x := range v.L {
			out[i] = scrub(x)
		}
		return Value{K: List, L: out}
	case Object:
		out := make([]Member, len(v.O))
		for i, m := range v.O {
			out[i] = Member{m.Key, scrub(m.Val)}
		}
		return Value{K: Object, O: out}
	}
	return v
}
