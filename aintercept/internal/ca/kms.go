package ca

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// KMSAPI is the subset of the KMS client the signer uses, so tests can stub it.
type KMSAPI interface {
	GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, optFns ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(ctx context.Context, in *kms.SignInput, optFns ...func(*kms.Options)) (*kms.SignOutput, error)
}

// kmsSigner is a crypto.Signer whose private key lives in AWS KMS and cannot be
// exported. x509.CreateCertificate hands it the leaf's TBS digest, which it
// sends to KMS to sign; every signature is a KMS API call, and so recorded in
// CloudTrail.
type kmsSigner struct {
	client KMSAPI
	keyID  string
	pub    crypto.PublicKey
}

// kmsSignTimeout bounds one KMS Sign call. Leaves are minted rarely (once per
// host, then on renewal), so a slow call delays only that mint.
const kmsSignTimeout = 10 * time.Second

// NewKMSSigner returns a signer for the asymmetric SIGN_VERIFY KMS key keyID
// (key ID, ARN, or alias). RSA and ECC NIST keys are supported.
func NewKMSSigner(ctx context.Context, client KMSAPI, keyID string) (crypto.Signer, error) {
	out, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		return nil, fmt.Errorf("kms GetPublicKey %s: %w", keyID, err)
	}
	pub, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("kms public key for %s: %w", keyID, err)
	}
	switch pub.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
	default:
		return nil, fmt.Errorf("kms key %s: unsupported public key type %T", keyID, pub)
	}
	return &kmsSigner{client: client, keyID: keyID, pub: pub}, nil
}

func (s *kmsSigner) Public() crypto.PublicKey { return s.pub }

func (s *kmsSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	alg, err := kmsAlgorithm(s.pub, opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kmsSignTimeout)
	defer cancel()
	out, err := s.client.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(s.keyID),
		Message:          digest,
		MessageType:      kmstypes.MessageTypeDigest,
		SigningAlgorithm: alg,
	})
	if err != nil {
		return nil, fmt.Errorf("kms Sign: %w", err)
	}
	// KMS returns PKCS#1 v1.5 / PSS bytes for RSA and an ASN.1 DER signature
	// for ECDSA: the same encodings crypto.Signer callers expect.
	return out.Signature, nil
}

// kmsAlgorithm maps the key type and the caller's hash (and PSS choice) to the
// KMS signing algorithm.
func kmsAlgorithm(pub crypto.PublicKey, opts crypto.SignerOpts) (kmstypes.SigningAlgorithmSpec, error) {
	_, pss := opts.(*rsa.PSSOptions)
	h := opts.HashFunc()
	switch pub.(type) {
	case *rsa.PublicKey:
		switch {
		case pss && h == crypto.SHA256:
			return kmstypes.SigningAlgorithmSpecRsassaPssSha256, nil
		case pss && h == crypto.SHA384:
			return kmstypes.SigningAlgorithmSpecRsassaPssSha384, nil
		case pss && h == crypto.SHA512:
			return kmstypes.SigningAlgorithmSpecRsassaPssSha512, nil
		case h == crypto.SHA256:
			return kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha256, nil
		case h == crypto.SHA384:
			return kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha384, nil
		case h == crypto.SHA512:
			return kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha512, nil
		}
	case *ecdsa.PublicKey:
		switch h {
		case crypto.SHA256:
			return kmstypes.SigningAlgorithmSpecEcdsaSha256, nil
		case crypto.SHA384:
			return kmstypes.SigningAlgorithmSpecEcdsaSha384, nil
		case crypto.SHA512:
			return kmstypes.SigningAlgorithmSpecEcdsaSha512, nil
		}
	}
	return "", fmt.Errorf("no KMS signing algorithm for %T with hash %v (PSS=%v)", pub, h, pss)
}
