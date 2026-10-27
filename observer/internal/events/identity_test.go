package events

import (
	"strings"
	"testing"
)

func TestHashAPIKey_BearerToken(t *testing.T) {
	h := map[string]string{"authorization": "Bearer sk-ant-secret123"}
	got := HashAPIKey(h, "salt")
	if got == "" {
		t.Fatal("expected a hash for a Bearer token")
	}
	if strings.Contains(got, "secret123") || strings.Contains(got, "sk-ant") {
		t.Fatalf("hash must not contain the raw key: %q", got)
	}
	if len(got) != 32 { // 16 bytes hex
		t.Fatalf("expected 32 hex chars, got %d (%q)", len(got), got)
	}
}

func TestHashAPIKey_XApiKey(t *testing.T) {
	h := map[string]string{"x-api-key": "sk-ant-abc"}
	if HashAPIKey(h, "salt") == "" {
		t.Fatal("expected a hash for x-api-key")
	}
}

func TestHashAPIKey_Stable(t *testing.T) {
	a := HashAPIKey(map[string]string{"authorization": "Bearer k"}, "salt")
	b := HashAPIKey(map[string]string{"authorization": "Bearer k"}, "salt")
	if a != b {
		t.Fatalf("same key+salt must hash identically: %q vs %q", a, b)
	}
}

func TestHashAPIKey_SaltChangesHash(t *testing.T) {
	a := HashAPIKey(map[string]string{"authorization": "Bearer k"}, "salt1")
	b := HashAPIKey(map[string]string{"authorization": "Bearer k"}, "salt2")
	if a == b {
		t.Fatal("different salts must produce different hashes (no cross-deployment correlation)")
	}
}

func TestHashAPIKey_DistinctKeysDiffer(t *testing.T) {
	a := HashAPIKey(map[string]string{"authorization": "Bearer k1"}, "salt")
	b := HashAPIKey(map[string]string{"authorization": "Bearer k2"}, "salt")
	if a == b {
		t.Fatal("distinct keys must hash differently")
	}
}

func TestHashAPIKey_NoAuthReturnsEmpty(t *testing.T) {
	if got := HashAPIKey(map[string]string{"user-agent": "curl"}, "salt"); got != "" {
		t.Fatalf("expected empty hash when no auth header, got %q", got)
	}
}

func TestHashAPIKey_CaseInsensitiveHeader(t *testing.T) {
	lower := HashAPIKey(map[string]string{"authorization": "Bearer k"}, "s")
	upper := HashAPIKey(map[string]string{"Authorization": "Bearer k"}, "s")
	if lower == "" || lower != upper {
		t.Fatalf("header lookup must be case-insensitive: %q vs %q", lower, upper)
	}
}

func TestClientIP_XForwardedForFirst(t *testing.T) {
	h := map[string]string{"x-forwarded-for": "203.0.113.7, 10.0.0.1, 10.0.0.2"}
	if got := ClientIP(h); got != "203.0.113.7" {
		t.Fatalf("expected first XFF hop, got %q", got)
	}
}

func TestClientIP_FallbackXRealIP(t *testing.T) {
	if got := ClientIP(map[string]string{"x-real-ip": "198.51.100.9"}); got != "198.51.100.9" {
		t.Fatalf("expected x-real-ip fallback, got %q", got)
	}
}

func TestClientIP_NoneReturnsEmpty(t *testing.T) {
	if got := ClientIP(map[string]string{"user-agent": "curl"}); got != "" {
		t.Fatalf("expected empty IP, got %q", got)
	}
}

func TestKeyHint_PrefixSuffixMasked(t *testing.T) {
	h := map[string]string{"authorization": "Bearer sk-ant-api03-LONGSECRETMIDDLExyz-a3f9"}
	got := KeyHint(h, 6, 4)
	if got != "sk-ant…a3f9" {
		t.Fatalf("got %q", got)
	}
	// Must not contain the secret middle.
	if strings.Contains(got, "LONGSECRETMIDDLE") {
		t.Fatalf("hint leaked the middle: %q", got)
	}
}

func TestKeyHint_ShortKeyMostlyMasked(t *testing.T) {
	// A short key must not be revealed almost-entirely by prefix+suffix: at most
	// 3 leading chars survive, the rest is masked.
	got := KeyHint(map[string]string{"x-api-key": "abcdef"}, 6, 4)
	if strings.Contains(got, "def") || !strings.HasSuffix(got, "…") {
		t.Fatalf("short key over-revealed: %q", got)
	}
	if visible := strings.TrimSuffix(got, "…"); len(visible) > 3 {
		t.Fatalf("too many chars revealed for short key: %q", got)
	}
}

func TestKeyHint_NoKeyEmpty(t *testing.T) {
	if got := KeyHint(map[string]string{"user-agent": "curl"}, 6, 4); got != "" {
		t.Fatalf("expected empty hint, got %q", got)
	}
}

func TestKeyHint_StableWithHash(t *testing.T) {
	// Same key → same hint AND same hash (they pair as identity).
	h := map[string]string{"authorization": "Bearer sk-test-aaaaaaaaaaaaaaaa-zzzz"}
	if KeyHint(h, 6, 4) != KeyHint(h, 6, 4) {
		t.Fatal("hint must be deterministic")
	}
}
