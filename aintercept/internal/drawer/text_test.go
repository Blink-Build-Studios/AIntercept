package drawer

import (
	"testing"
)

// Python's semantics where the drawer's output depends on them; each want is
// what CPython gives.

func TestRedactIsPythons(t *testing.T) {
	tok := "Bearer abcdefghijklmnop"
	for in, want := range map[string]string{
		"x " + tok + " y":         "x [redacted] y",
		"x" + tok:                 "x" + tok, // \b: a word character before
		"é" + tok:                 "é" + tok, // Python's \w is Unicode's
		"-" + tok:                 "-[redacted]",
		"Bearer abcdefghijklmnop": "[redacted]", // \s is Unicode's
		"key sk-ant-abcdefgh1 and ghp_" + "abcdefghijklmnopqrstu": "key [redacted] and [redacted]",
		`{"access_token" : "zzz", "a": 1}`:                        `{[redacted], "a": 1}`,
		"eyJabcdefgh.abcdefgh.abcdefgh":                           "[redacted]",
		"nothing here":                                            "nothing here",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeReplaceIsPythons(t *testing.T) {
	for in, want := range map[string]string{
		"a\xe2\x82":     "a�", // a truncated sequence: one replacement
		"\xe2\x82\xac":  "€",
		"\xff\xfe":      "��",
		"\xf0\x9f\x98x": "�x",
		"\xed\xa0\x80":  "���", // a surrogate's encoding
	} {
		if got := decodeReplace([]byte(in)); got != want {
			t.Errorf("decode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitLinesAndDumpsArePythons(t *testing.T) {
	if got := splitLines("a\r\nb\rc\x1cd e\n"); len(got) != 5 || got[4] != "e" {
		t.Errorf("splitlines: %q", got)
	}
	v, _ := Loads([]byte(`{"b":[1,2.5,1e20,"é\u0001"],"a":{},"c":null,"b":true}`))
	if got, want := Dumps(v, ", ", ": ", -1), `{"b": true, "a": {}, "c": null}`; got != want {
		t.Errorf("dumps: %s, want %s", got, want)
	}
	v, _ = Loads([]byte(`{"x":[1,2.5,1e20,"é\u0001\u007f"]}`))
	if got, want := Dumps(v, ",", ":", -1), `{"x":[1,2.5,1e+20,"\u00e9\u0001\u007f"]}`; got != want {
		t.Errorf("dumps: %s, want %s", got, want)
	}
	if got, want := Dumps(v, ", ", ": ", 2), "{\n  \"x\": [\n    1,\n    2.5,\n    1e+20,\n    \"\\u00e9\\u0001\\u007f\"\n  ]\n}"; got != want {
		t.Errorf("dumps indent: %s", got)
	}
	for f, want := range map[float64]string{1: "1.0", 0.1: "0.1", 1e16: "1e+16", 1e-5: "1e-05", 123456.789: "123456.789"} {
		if got := pyRepr(f); got != want {
			t.Errorf("repr(%v) = %s", f, got)
		}
	}
	for ms, want := range map[float64]string{400: "0.4 s", 59_960: "60.0 s", 61_500: "1m 02s", 62_500: "1m 02s", 125_000: "2m 05s"} {
		if got := secs(ms); got != want {
			t.Errorf("secs(%v) = %s, want %s", ms, got, want)
		}
	}
}
