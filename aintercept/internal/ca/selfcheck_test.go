package ca

import (
	"crypto/x509"
	"strings"
	"testing"
)

func newTestIssuer(t *testing.T, permitted []string) *Issuer {
	t.Helper()
	rootKey, rootCert := makeCA(t, "test root", nil, nil)
	intKey, intCert := makeConstrainedCA(t, "test intermediate", rootCert, rootKey, permitted)
	iss, err := NewIssuer(writeCA(t, rootCert, intCert, intKey))
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

func TestSelfCheckConstrainedCoversAllowlist(t *testing.T) {
	iss := newTestIssuer(t, []string{"anthropic.com", "claude.ai"})
	constrained, err := iss.SelfCheck([]string{"api.anthropic.com", "claude.ai"}, true)
	if err != nil || !constrained {
		t.Fatalf("want constrained, no error; got %v, %v", constrained, err)
	}
}

func TestSelfCheckRejectsHostOutsideConstraints(t *testing.T) {
	iss := newTestIssuer(t, []string{"anthropic.com"})
	_, err := iss.SelfCheck([]string{"api.anthropic.com", "chatgpt.com"}, false)
	if err == nil || !strings.Contains(err.Error(), "chatgpt.com") {
		t.Fatalf("want an error naming chatgpt.com, got %v", err)
	}
}

func TestSelfCheckUnconstrained(t *testing.T) {
	iss := newTestIssuer(t, nil)
	constrained, err := iss.SelfCheck([]string{"api.anthropic.com"}, false)
	if err != nil || constrained {
		t.Fatalf("want unconstrained, no error; got %v, %v", constrained, err)
	}
	if _, err := iss.SelfCheck([]string{"api.anthropic.com"}, true); err == nil {
		t.Fatal("requireConstraints must reject an unconstrained chain")
	}
}

func TestSelfCheckDoesNotCache(t *testing.T) {
	iss := newTestIssuer(t, nil)
	if _, err := iss.SelfCheck([]string{"api.anthropic.com"}, false); err != nil {
		t.Fatal(err)
	}
	if len(iss.cache) != 0 {
		t.Fatal("self-check leaves must not be served")
	}
}

func TestSelfCheckWildcardHost(t *testing.T) {
	iss := newTestIssuer(t, []string{"oaiusercontent.com"})
	if _, err := iss.SelfCheck([]string{"*.oaiusercontent.com"}, true); err != nil {
		t.Fatalf("wildcard allowlist entry: %v", err)
	}
}

func TestSelfCheckExcludedOnlyIsNotConstrained(t *testing.T) {
	rootKey, rootCert := makeCA(t, "test root", nil, nil)
	intKey, intCert := makeCAWith(t, "test intermediate", rootCert, rootKey, func(c *x509.Certificate) {
		c.ExcludedDNSDomains = []string{"mil"}
	})
	iss, err := NewIssuer(writeCA(t, rootCert, intCert, intKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iss.SelfCheck([]string{"api.anthropic.com"}, true); err == nil {
		t.Fatal("an intermediate with only excluded names can sign for almost anything; requireConstraints must reject it")
	}
}
