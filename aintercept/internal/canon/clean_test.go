package canon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// Postgres text can't hold NUL, and a btree entry over about 2.7 KB fails its
// insert (docs/PIPELINE.md §3.1): what canon does to a value so neither reaches it.

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"a\x00b", "a\uFFFDb"},
		{"\x00\x00", "\uFFFD\uFFFD"},
		{"é\x00日本", "é\uFFFD日本"},
		{`a\u0000b`, `a\u0000b`}, // text, not JSON: its characters are kept
	}
	for _, c := range cases {
		if got := CleanText(c.in); got != c.want {
			t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":"b"}`, `{"a":"b"}`},
		{`{"a":"x\u0000y"}`, `{"a":"x\ufffdy"}`},
		{`{"a\u0000":"\u0000"}`, `{"a\ufffd":"\ufffd"}`},
		{`{"a":"x\\u0000y"}`, `{"a":"x\\u0000y"}`},       // an escaped backslash, then text
		{`{"a":"x\\\u0000y"}`, `{"a":"x\\\ufffdy"}`},     // an escaped backslash, then a NUL
		{`{"a":"\u00001"}`, `{"a":"\ufffd1"}`},           // only the four digits
		{`{"a":"\u0001"}`, `{"a":"\u0001"}`},             // other escapes stay
		{"{\"a\":\"raw\x00\"}", "{\"a\":\"raw\uFFFD\"}"}, // a raw NUL too
		{`{"a":"ends\`, `{"a":"ends\`},                   // truncated: kept as it is
	}
	for _, c := range cases {
		if got := CleanJSON(c.in); got != c.want {
			t.Errorf("CleanJSON(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// What a decoder then reads holds no NUL, and the key order is the client's.
	var v map[string]string
	if err := json.Unmarshal([]byte(CleanJSON(`{"z":"a\u0000","a":"b"}`)), &v); err != nil || strings.ContainsRune(v["z"], 0) {
		t.Fatalf("decoded %q, %v", v, err)
	}
}

func TestCleanID(t *testing.T) {
	at := strings.Repeat("x", MaxIDBytes)
	over := at + "y"
	sum := sha256.Sum256([]byte(over))
	hashed := "sha256:" + hex.EncodeToString(sum[:])
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"short", "s1", "s1"},
		{"exactly the cap", at, at},
		{"one over", over, hashed},
		{"5 KB", strings.Repeat("é", 2560), ""},
		{"NUL", "s\x001", "s\uFFFD1"},
	}
	for _, c := range cases {
		got := CleanID(c.in)
		if c.want == "" && c.in != "" {
			if !strings.HasPrefix(got, "sha256:") || len(got) != 7+64 {
				t.Errorf("%s: CleanID = %q, want sha256:<hex>", c.name, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: CleanID(%q…) = %q, want %q", c.name, c.in[:min(len(c.in), 10)], got, c.want)
		}
	}
	// NUL is replaced before the length is measured, and the result is stable.
	nul := strings.Repeat("\x00", MaxIDBytes)
	if got := CleanID(nul); !strings.HasPrefix(got, "sha256:") {
		t.Errorf("512 NULs are 1,536 bytes as U+FFFD: want hashed, got %d bytes", len(got))
	}
	for _, in := range []string{"s1", over, nul, "a\x00b"} {
		once := CleanID(in)
		if twice := CleanID(once); twice != once {
			t.Errorf("CleanID isn't idempotent: %q then %q", once, twice)
		}
		if strings.ContainsRune(once, 0) || len(once) > MaxIDBytes {
			t.Errorf("CleanID(%q…) = %q", in[:min(len(in), 10)], once)
		}
	}
}

func TestCleanLeavesCleanValuesAlone(t *testing.T) {
	// The common case allocates nothing: the same string comes back.
	s := strings.Repeat("abc", 100)
	if n := testing.AllocsPerRun(100, func() { _ = CleanText(s); _ = CleanID(s); _ = CleanJSON(s) }); n != 0 {
		t.Errorf("%v allocations for a clean value", n)
	}
}
