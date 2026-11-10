package certsvc

import "testing"

func TestAllowlistPermits(t *testing.T) {
	a := parseAllowlist("api.anthropic.com, *.oaiusercontent.com")
	for host, want := range map[string]bool{
		"api.anthropic.com":                   true,
		"API.Anthropic.com":                   true,
		"*.oaiusercontent.com":                true, // the wildcard entry itself
		"sdmntprcentralus.oaiusercontent.com": true,
		"a.b.oaiusercontent.com":              true,
		"oaiusercontent.com":                  false,
		"evil.com/.oaiusercontent.com":        false,
		"..oaiusercontent.com":                false,
		"*.*.oaiusercontent.com":              false,
		"-x.oaiusercontent.com":               false,
		"x_y.oaiusercontent.com":              false,
		"evil.com":                            false,
		"api.anthropic.com.evil.com":          false,
	} {
		if got := a.permits(host); got != want {
			t.Errorf("permits(%q) = %v, want %v", host, got, want)
		}
	}
}
