package ca

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
)

// makeCA builds a CA cert/key. If parent/parentKey are nil it is self-signed
// (root); otherwise it is signed by the parent (intermediate).
func makeCA(t *testing.T, cn string, parent *x509.Certificate, parentKey *rsa.PrivateKey) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	return makeConstrainedCA(t, cn, parent, parentKey, nil)
}

// makeConstrainedCA is makeCA with permitted DNS name constraints (none if nil).
func makeConstrainedCA(t *testing.T, cn string, parent *x509.Certificate, parentKey *rsa.PrivateKey, permitted []string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	return makeCAWith(t, cn, parent, parentKey, func(c *x509.Certificate) { c.PermittedDNSDomains = permitted })
}

// makeCAWith is makeCA with the template adjusted by edit before signing.
func makeCAWith(t *testing.T, cn string, parent *x509.Certificate, parentKey *rsa.PrivateKey, edit func(*x509.Certificate)) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		MaxPathLenZero:        parent != nil,
	}
	edit(tmpl)
	signer := tmpl
	signerKey := key
	if parent != nil {
		signer = parent
		signerKey = parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// writeCA writes intermediate cert/key and an intermediate+root chain to a temp
// dir and returns a Config pointing at them.
func writeCA(t *testing.T, rootCert, intCert *x509.Certificate, intKey *rsa.PrivateKey) Config {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		IntermediateCertPath: filepath.Join(dir, "intermediate-ca.crt"),
		IntermediateKeyPath:  filepath.Join(dir, "intermediate-ca.key"),
		ChainPath:            filepath.Join(dir, "ca-chain.crt"),
		LeafTTL:              time.Hour,
		LeafBits:             2048,
	}
	writePEM(t, cfg.IntermediateCertPath, "CERTIFICATE", intCert.Raw)
	writePEM(t, cfg.IntermediateKeyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(intKey))
	chain := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intCert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootCert.Raw})...,
	)
	if err := os.WriteFile(cfg.ChainPath, chain, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
