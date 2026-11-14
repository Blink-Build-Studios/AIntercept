package drawer

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// redactScan is the redaction as first ported (v7): the whole text run
// through SECRET_TEXT's alternation, one NFA pass, then each alternative tried
// at the match in order. It's kept here as the reference redact must equal.
func redactScan(s string) string {
	if !secretAnyRef.MatchString(s) {
		return s
	}
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		loc := secretAnyRef.FindStringIndex(s[pos:])
		if loc == nil {
			break
		}
		q := pos + loc[0]
		end := -1
		for i, re := range secretAt {
			if bounded[i] && q > 0 {
				r, _ := utf8.DecodeLastRuneInString(s[:q])
				if isWord(r) {
					continue
				}
			}
			if m := re.FindStringIndex(s[q:]); m != nil {
				end = q + m[1]
				break
			}
		}
		if end < 0 {
			_, w := utf8.DecodeRuneInString(s[q:])
			pos = q + w
			continue
		}
		b.WriteString(s[last:q])
		b.WriteString("[redacted]")
		last, pos = end, end
	}
	b.WriteString(s[last:])
	return b.String()
}

var secretAnyRef = regexp.MustCompile(strings.Join(secretAlts, "|"))

// The pieces the generated texts are made of: every alternative's start, whole
// and cut short, the characters its classes stop at, Python's \s and \w
// beyond ASCII, and filler.
var redactPieces = []string{
	"sk-ant-", "sk-ant", "sk-", "abcdefgh", "ABCD1234", "_-_-", "Bearer", "Bearer ", "Bearer  ",
	"bearer ", "abcdefghijkl", "x.y~z+w/v-u", "==", "=", "eyJ", "eyJabcdefgh", ".", "..", "gh", "ghp_", "ghs_",
	"ghx_", "0123456789abcdefghij", "xox", "xoxb-", "xoxq-", "ya29.", "ya29", `"access_token"`, `"refresh_token"`,
	`"id_token"`, `"client_secret"`, `"token"`, " : ", ":", `"`, `"zzz"`, `\"`, " ", "\n", "\t", "\r\n", "\x1c",
	"\u0085", "é", "日本", "١٢", "Ⅻ", "_", "a", "-", "{", "}", ",", "high though right", "�",
}

func genRedactText(r *rand.Rand) string {
	var b strings.Builder
	for n := r.Intn(24); n >= 0; n-- {
		b.WriteString(redactPieces[r.Intn(len(redactPieces))])
	}
	return b.String()
}

func TestRedactEqualsTheScan(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	n := 50_000
	if testing.Short() {
		n = 5_000
	}
	for i := 0; i < n; i++ {
		s := genRedactText(r)
		if got, want := redact(s), redactScan(s); got != want {
			t.Fatalf("redact(%q)\n = %q\nwant %q", s, got, want)
		}
	}
	big := bigBody(1 << 20)
	if redact(big) != redactScan(big) {
		t.Fatal("a large body redacts differently")
	}
}

func FuzzRedact(f *testing.F) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 64; i++ {
		f.Add(genRedactText(r))
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return // redact's input is always text (decodeReplace's, or a JSON string's)
		}
		if got, want := redact(s), redactScan(s); got != want {
			t.Fatalf("redact(%q) = %q, want %q", s, got, want)
		}
	})
}

// bigBody is a request body's shape: a long JSON conversation of prose, code
// and tool output, with a few credentials in it.
func bigBody(size int) string {
	r := rand.New(rand.NewSource(3))
	words := []string{"the", "high", "though", "right", "eyes", "graph", "box", "yard", "skill", "Bearer", "token",
		"func", "return", "err", "nil", "\\n", "\\\"", "{", "}", "é", "日本語", "gh", "xo", "ya", "sk"}
	var b strings.Builder
	b.WriteString(`{"model":"m","messages":[`)
	for b.Len() < size {
		b.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"`)
		for k := 0; k < 400; k++ {
			b.WriteString(words[r.Intn(len(words))])
			b.WriteByte(' ')
		}
		if r.Intn(8) == 0 {
			b.WriteString(`Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123 `)
		}
		b.WriteString(`"}]},`)
	}
	b.WriteString(`{"role":"user","content":"x"}]}`)
	return b.String()
}

func BenchmarkRedact(b *testing.B) {
	body := bigBody(4 << 20)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = redact(body)
	}
}
