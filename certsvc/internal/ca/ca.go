// Package ca mints short-lived leaf certificates for allowlisted SNI hostnames,
// signed by the issuing intermediate CA. The root private key is never loaded
// here; only the intermediate key and the full chain are used.
package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"
)

// Issuer holds the intermediate signing key and the chain used to assemble leaf
// bundles. It mints and caches one leaf per hostname.
type Issuer struct {
	intermediateCert *x509.Certificate
	intermediateKey  *rsa.PrivateKey
	// chainPEM is intermediate + root, appended after each leaf so Envoy serves
	// a complete chain to the client.
	chainPEM []byte
	leafTTL  time.Duration
	leafBits int

	mu    sync.Mutex
	cache map[string]*Leaf // keyed by hostname
}

// Leaf is a minted certificate bundle: the leaf cert + chain, and its key, both
// PEM-encoded, plus the expiry for cache validity checks.
type Leaf struct {
	CertChainPEM []byte
	PrivateKey   []byte
	NotAfter     time.Time
}

// Config configures an Issuer.
type Config struct {
	IntermediateCertPath string
	IntermediateKeyPath  string
	// ChainPath is intermediate + root concatenated (ca-chain.crt). Appended to
	// each leaf. If empty, only the intermediate cert is appended.
	ChainPath string
	LeafTTL   time.Duration
	LeafBits  int
}

// NewIssuer loads the intermediate key/cert and prepares the chain. It never
// loads or references the root private key.
func NewIssuer(cfg Config) (*Issuer, error) {
	certPEM, err := os.ReadFile(cfg.IntermediateCertPath)
	if err != nil {
		return nil, fmt.Errorf("read intermediate cert: %w", err)
	}
	keyPEM, err := os.ReadFile(cfg.IntermediateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read intermediate key: %w", err)
	}

	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse intermediate cert: %w", err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("intermediate cert is not a CA")
	}
	key, err := parseRSAKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse intermediate key: %w", err)
	}

	chainPEM := certPEM
	if cfg.ChainPath != "" {
		chainPEM, err = os.ReadFile(cfg.ChainPath)
		if err != nil {
			return nil, fmt.Errorf("read chain: %w", err)
		}
	}

	ttl := cfg.LeafTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	bits := cfg.LeafBits
	if bits == 0 {
		bits = 2048
	}

	return &Issuer{
		intermediateCert: cert,
		intermediateKey:  key,
		chainPEM:         chainPEM,
		leafTTL:          ttl,
		leafBits:         bits,
		cache:            make(map[string]*Leaf),
	}, nil
}

// LeafFor returns a valid leaf for host, minting and caching one if needed.
// Concurrent callers for the same host mint only once. The caller is
// responsible for ensuring host is allowlisted; LeafFor does not enforce policy.
func (i *Issuer) LeafFor(host string) (*Leaf, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if existing, ok := i.cache[host]; ok && time.Until(existing.NotAfter) > i.leafTTL/10 {
		return existing, nil
	}

	leaf, err := i.mint(host)
	if err != nil {
		return nil, err
	}
	i.cache[host] = leaf
	return leaf, nil
}

// ForceLeafFor always mints a FRESH leaf for host, ignoring (and replacing) any
// cached one. The proactive renewer uses this so a renewal is a genuine new
// certificate: the plain LeafFor cache threshold (leafTTL/10) is tighter than the
// renewer's renewBefore window (leafTTL/4), so LeafFor would otherwise hand back
// the very near-expiry leaf we are trying to replace.
func (i *Issuer) ForceLeafFor(host string) (*Leaf, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	leaf, err := i.mint(host)
	if err != nil {
		return nil, err
	}
	i.cache[host] = leaf
	return leaf, nil
}

// mint creates a fresh server-auth leaf for host. Caller holds i.mu.
func (i *Issuer) mint(host string) (*Leaf, error) {
	key, err := rsa.GenerateKey(rand.Reader, i.leafBits)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-1 * time.Minute),
		NotAfter:     now.Add(i.leafTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
		// Explicitly not a CA.
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, i.intermediateCert, &key.PublicKey, i.intermediateKey)
	if err != nil {
		return nil, fmt.Errorf("sign leaf: %w", err)
	}

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle := append(append([]byte{}, leafPEM...), i.chainPEM...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	return &Leaf{
		CertChainPEM: bundle,
		PrivateKey:   keyPEM,
		NotAfter:     tmpl.NotAfter,
	}, nil
}

func parseCert(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseRSAKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("intermediate key is not RSA")
	}
	return rsaKey, nil
}
