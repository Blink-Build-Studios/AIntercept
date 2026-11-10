package events

import (
	"strings"
	"testing"
)

const geminiTestKey = "AIzaSyTEST-not-a-real-key-0123456789"

func TestRedactPathRedactsCredentialQueryParams(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/v1beta/models/gemini-2.0-flash:generateContent", "/v1beta/models/gemini-2.0-flash:generateContent"},
		{"/v1beta/models/gemini-2.0-flash:generateContent?key=" + geminiTestKey,
			"/v1beta/models/gemini-2.0-flash:generateContent?key=" + redactedMarker},
		{"/v1beta/models/g:streamGenerateContent?alt=sse&key=" + geminiTestKey,
			"/v1beta/models/g:streamGenerateContent?alt=sse&key=" + redactedMarker},
		{"/x?KEY=" + geminiTestKey + "&alt=sse", "/x?KEY=" + redactedMarker + "&alt=sse"},
		{"/x?api_key=a&apikey=b&access_token=c&keep=d",
			"/x?api_key=" + redactedMarker + "&apikey=" + redactedMarker + "&access_token=" + redactedMarker + "&keep=d"},
		// Percent-encoded parameter name still matches.
		{"/x?%6Bey=" + geminiTestKey, "/x?%6Bey=" + redactedMarker},
		// A fragment survives; a valueless flag is left alone.
		{"/x?key=" + geminiTestKey + "#frag", "/x?key=" + redactedMarker + "#frag"},
		{"/x?key", "/x?key"},
		// Lookalike names are not credentials.
		{"/x?keyword=hello&monkey=1", "/x?keyword=hello&monkey=1"},
	}
	for _, c := range cases {
		if got := RedactPath(c.in); got != c.want {
			t.Errorf("RedactPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactHeadersCoversGeminiKeyAndPath(t *testing.T) {
	in := map[string]string{
		"x-goog-api-key": geminiTestKey,
		":path":          "/v1beta/models/g:generateContent?key=" + geminiTestKey,
		":authority":     "generativelanguage.googleapis.com",
	}
	out := RedactHeaders(in)
	for k, v := range out {
		if strings.Contains(v, geminiTestKey) {
			t.Fatalf("header %q still carries the key: %q", k, v)
		}
	}
	if out["x-goog-api-key"] != redactedMarker {
		t.Fatalf("x-goog-api-key = %q, want the marker", out["x-goog-api-key"])
	}
	if out[":authority"] != in[":authority"] {
		t.Fatalf(":authority changed: %q", out[":authority"])
	}
	if in["x-goog-api-key"] != geminiTestKey {
		t.Fatal("RedactHeaders mutated its input")
	}
	if !IsSensitiveHeader("X-Goog-Api-Key") {
		t.Fatal("x-goog-api-key must be a sensitive header")
	}
}

// Both ways Gemini accepts a key identify the same caller.
func TestHashAPIKeyRecognizesGeminiForms(t *testing.T) {
	fromHeader := HashAPIKey(map[string]string{"x-goog-api-key": geminiTestKey}, "salt")
	fromQuery := HashAPIKey(map[string]string{":path": "/v1beta/models/g:generateContent?alt=sse&key=" + geminiTestKey}, "salt")
	if fromHeader == "" || fromQuery == "" {
		t.Fatalf("no hash: header=%q query=%q", fromHeader, fromQuery)
	}
	if fromHeader != fromQuery {
		t.Fatalf("header and query forms hash differently: %q vs %q", fromHeader, fromQuery)
	}
	if got := HashAPIKey(map[string]string{":path": "/v1beta/models"}, "salt"); got != "" {
		t.Fatalf("a path with no key hashed to %q", got)
	}
	hint := KeyHint(map[string]string{"x-goog-api-key": geminiTestKey}, 6, 4)
	if hint == "" || strings.Contains(hint, geminiTestKey[6:len(geminiTestKey)-4]) {
		t.Fatalf("hint %q is empty or reveals the key's middle", hint)
	}
}
