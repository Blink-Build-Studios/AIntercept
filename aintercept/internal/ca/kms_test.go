package ca

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// fakeKMS signs with a local key, standing in for KMS: it receives only the
// digest and algorithm, as the real service does.
type fakeKMS struct {
	key   crypto.Signer
	calls int
}

func (f *fakeKMS) GetPublicKey(_ context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(f.key.Public())
	return &kms.GetPublicKeyOutput{PublicKey: der}, err
}

func (f *fakeKMS) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	f.calls++
	var opts crypto.SignerOpts
	switch in.SigningAlgorithm {
	case kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha256, kmstypes.SigningAlgorithmSpecEcdsaSha256:
		opts = crypto.SHA256
	case kmstypes.SigningAlgorithmSpecEcdsaSha384:
		opts = crypto.SHA384
	default:
		return nil, &kmstypes.UnsupportedOperationException{Message: aws.String(string(in.SigningAlgorithm))}
	}
	sig, err := f.key.Sign(rand.Reader, in.Message, opts)
	return &kms.SignOutput{Signature: sig}, err
}

// writeKMSBackedCA issues an intermediate for pub (a KMS key's public key),
// signed by a fresh local root and name-constrained to permitted, and writes
// the cert and chain. There is no intermediate key file: KMS holds it.
func writeKMSBackedCA(t *testing.T, pub crypto.PublicKey, permitted []string) Config {
	t.Helper()
	rootKey, rootCert := makeCA(t, "test root", nil, nil)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "kms intermediate"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomains:   permitted,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, pub, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := Config{
		IntermediateCertPath: filepath.Join(dir, "intermediate-ca.crt"),
		ChainPath:            filepath.Join(dir, "ca-chain.crt"),
		LeafTTL:              time.Hour,
		LeafBits:             2048,
	}
	writePEM(t, cfg.IntermediateCertPath, "CERTIFICATE", der)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootCert.Raw})...)
	if err := os.WriteFile(cfg.ChainPath, chain, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// exerciseKMSIssuer runs the startup self-check and mints a served leaf
// through a KMS-backed issuer, verifying the leaf like a client would.
func exerciseKMSIssuer(t *testing.T, client KMSAPI, keyID string) {
	t.Helper()
	signer, err := NewKMSSigner(context.Background(), client, keyID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := writeKMSBackedCA(t, signer.Public(), []string{"anthropic.com"})
	cfg.Signer = signer
	iss, err := NewIssuer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if constrained, err := iss.SelfCheck([]string{"api.anthropic.com"}, true); err != nil || !constrained {
		t.Fatalf("self-check: constrained=%v err=%v", constrained, err)
	}
	if _, err := iss.SelfCheck([]string{"chatgpt.com"}, true); err == nil {
		t.Fatal("self-check must reject a host outside the KMS intermediate's constraints")
	}
	leaf, err := iss.LeafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	certs, err := parseChain(leaf.CertChainPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(certs[len(certs)-1])
	inters.AddCert(certs[1])
	if _, err := certs[0].Verify(x509.VerifyOptions{DNSName: "api.anthropic.com", Roots: roots, Intermediates: inters}); err != nil {
		t.Fatalf("KMS-signed leaf does not verify: %v", err)
	}
}

func TestKMSSignerWithFake(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	for name, key := range map[string]crypto.Signer{"rsa": rsaKey, "p256": p256, "p384": p384} {
		t.Run(name, func(t *testing.T) {
			f := &fakeKMS{key: key}
			exerciseKMSIssuer(t, f, "alias/test")
			if f.calls == 0 {
				t.Fatal("expected signing to go through KMS")
			}
		})
	}
}

func TestNewIssuerRejectsMismatchedSigner(t *testing.T) {
	a, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cfg := writeKMSBackedCA(t, a.Public(), nil)
	cfg.Signer = b
	if _, err := NewIssuer(cfg); err == nil {
		t.Fatal("want an error when the signer doesn't match the intermediate certificate")
	}
}

// TestKMSSignerAgainstEmulator runs the same flow against a KMS API emulator
// (`make test-aws` starts one and sets CERTSVC_KMS_TEST_ENDPOINT). Use
// nsmithuk/local-kms: moto ignores MessageType=DIGEST and re-hashes the
// digest, so its signatures never verify (real KMS signs the digest as given).
func TestKMSSignerAgainstEmulator(t *testing.T) {
	endpoint := os.Getenv("CERTSVC_KMS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("CERTSVC_KMS_TEST_ENDPOINT not set")
	}
	client := kms.New(kms.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	for _, spec := range []kmstypes.KeySpec{kmstypes.KeySpecRsa2048, kmstypes.KeySpecEccNistP256, kmstypes.KeySpecEccNistP384} {
		t.Run(string(spec), func(t *testing.T) {
			out, err := client.CreateKey(context.Background(), &kms.CreateKeyInput{
				KeySpec:  spec,
				KeyUsage: kmstypes.KeyUsageTypeSignVerify,
			})
			if err != nil {
				t.Fatal(err)
			}
			exerciseKMSIssuer(t, client, *out.KeyMetadata.KeyId)
		})
	}
}

func TestKMSAlgorithm(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cases := []struct {
		pub  crypto.PublicKey
		opts crypto.SignerOpts
		want kmstypes.SigningAlgorithmSpec
	}{
		{&rsaKey.PublicKey, crypto.SHA256, kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha256},
		{&rsaKey.PublicKey, &rsa.PSSOptions{Hash: crypto.SHA384}, kmstypes.SigningAlgorithmSpecRsassaPssSha384},
		{&ec.PublicKey, crypto.SHA256, kmstypes.SigningAlgorithmSpecEcdsaSha256},
		{&ec.PublicKey, crypto.SHA512, kmstypes.SigningAlgorithmSpecEcdsaSha512},
	}
	for _, c := range cases {
		if got, err := kmsAlgorithm(c.pub, c.opts); err != nil || got != c.want {
			t.Errorf("kmsAlgorithm(%T, %v) = %v, %v; want %v", c.pub, c.opts, got, err, c.want)
		}
	}
	if _, err := kmsAlgorithm(&ec.PublicKey, crypto.SHA1); err == nil {
		t.Error("want an error for an unsupported hash")
	}
}
