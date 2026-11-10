package ca

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// writeTestCA creates a throwaway root+intermediate in a temp dir and returns a
// Config pointing at the intermediate. This mirrors generate-dev-ca.sh in Go so
// the test has no shell dependency.
func writeTestCA(t *testing.T) Config {
	t.Helper()
	rootKey, rootCert := makeCA(t, "test root", nil, nil)
	intKey, intCert := makeCA(t, "test intermediate", rootCert, rootKey)
	return writeCA(t, rootCert, intCert, intKey)
}

func TestLeafForMintsValidServerCert(t *testing.T) {
	issuer, err := NewIssuer(writeTestCA(t))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}

	leaf, err := issuer.LeafFor("api.anthropic.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}

	block, _ := pem.Decode(leaf.CertChainPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if cert.Subject.CommonName != "api.anthropic.com" {
		t.Errorf("CN = %q, want api.anthropic.com", cert.Subject.CommonName)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "api.anthropic.com" {
		t.Errorf("DNSNames = %v, want [api.anthropic.com]", cert.DNSNames)
	}
	if cert.IsCA {
		t.Error("leaf must not be a CA")
	}
	foundServerAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			foundServerAuth = true
		}
	}
	if !foundServerAuth {
		t.Error("leaf missing serverAuth EKU")
	}
}

func TestLeafForCachesAndReuses(t *testing.T) {
	issuer, err := NewIssuer(writeTestCA(t))
	if err != nil {
		t.Fatal(err)
	}
	a, err := issuer.LeafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := issuer.LeafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.CertChainPEM) != string(b.CertChainPEM) {
		t.Error("expected cached leaf to be reused for identical host")
	}
}
