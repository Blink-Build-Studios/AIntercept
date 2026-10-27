package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"

	"github.com/Blink-Build-Studios/aintercept/certsvc/internal/ca"
)

// newTestIssuer builds a self-signed intermediate CA on disk and returns an
// Issuer with the given leaf TTL.
func newTestIssuer(t *testing.T, leafTTL time.Duration) *ca.Issuer {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test intermediate"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "int.crt")
	keyPath := filepath.Join(dir, "int.key")
	writeFile(t, certPath, "CERTIFICATE", der)
	writeFile(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))

	issuer, err := ca.NewIssuer(ca.Config{
		IntermediateCertPath: certPath,
		IntermediateKeyPath:  keyPath,
		LeafTTL:              leafTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func writeFile(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestMinter(t *testing.T, leafTTL time.Duration) *minter {
	t.Helper()
	return &minter{
		issuer:      newTestIssuer(t, leafTTL),
		cache:       cachev3.NewLinearCache(resourcev3.SecretType),
		allow:       parseAllowlist("api.anthropic.com"),
		renewBefore: leafTTL / 4,
		issued:      map[string]time.Time{},
	}
}

func certBytes(t *testing.T, c *cachev3.LinearCache, host string) []byte {
	t.Helper()
	res := c.GetResources()
	r, ok := res[host]
	if !ok {
		t.Fatalf("no cached resource for %q", host)
	}
	sec, ok := r.(*tlsv3.Secret)
	if !ok {
		t.Fatalf("resource for %q is not a Secret", host)
	}
	return sec.GetTlsCertificate().GetCertificateChain().GetInlineBytes()
}

// A leaf within renewBefore of expiry is proactively re-minted with a FRESH cert
// and a later expiry — this is the fix for the "leaf aged out, Envoy served an
// expired cert" bug.
func TestRenewExpiring_RemintsNearExpiryLeaf(t *testing.T) {
	m := newTestMinter(t, 6*time.Hour) // renewBefore = 90m

	// First issue (on-demand path).
	if err := m.ensure([]string{"api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	before := certBytes(t, m.cache, "api.anthropic.com")

	// Force the recorded expiry to be inside the renewBefore window (as if time
	// had passed), so the next scan considers it due.
	m.mu.Lock()
	m.issued["api.anthropic.com"] = time.Now().Add(30 * time.Minute) // < 90m renewBefore
	m.mu.Unlock()

	m.renewExpiring()

	after := certBytes(t, m.cache, "api.anthropic.com")
	if string(before) == string(after) {
		t.Fatal("renewExpiring did not mint a FRESH cert (bytes unchanged) — the issuer cache likely short-circuited; ForceLeafFor should bypass it")
	}
	m.mu.Lock()
	exp := m.issued["api.anthropic.com"]
	m.mu.Unlock()
	if time.Until(exp) <= 90*time.Minute {
		t.Fatalf("renewed leaf expiry not advanced past renewBefore: %s", exp)
	}
}

// A leaf that is NOT near expiry is left untouched (no churn).
func TestRenewExpiring_LeavesFreshLeafAlone(t *testing.T) {
	m := newTestMinter(t, 6*time.Hour)
	if err := m.ensure([]string{"api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	before := certBytes(t, m.cache, "api.anthropic.com")
	// issued expiry is ~6h out, far outside the 90m renewBefore window.
	m.renewExpiring()
	after := certBytes(t, m.cache, "api.anthropic.com")
	if string(before) != string(after) {
		t.Fatal("renewExpiring re-minted a leaf that was not near expiry (unnecessary churn)")
	}
}

// ForceLeafFor always mints a distinct cert even when the issuer has a fresh
// cached leaf (the core of why renewal actually renews).
func TestForceLeafFor_BypassesIssuerCache(t *testing.T) {
	iss := newTestIssuer(t, 6*time.Hour)
	a, err := iss.LeafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := iss.ForceLeafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.CertChainPEM) == string(b.CertChainPEM) {
		t.Fatal("ForceLeafFor returned the cached cert; it must always mint fresh")
	}
}
