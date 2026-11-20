package certsvc

import (
	"crypto"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// flakySigner fails every signature while fail is set, as a KMS key certsvc
// isn't allowed to use would.
type flakySigner struct {
	crypto.Signer
	fail atomic.Bool
}

func (s *flakySigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if s.fail.Load() {
		return nil, errors.New("kms Sign: AccessDeniedException")
	}
	return s.Signer.Sign(r, digest, opts)
}

func readyStatus(r *readiness) int {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))
	return rec.Code
}

// certsvc is ready only once it has signed a leaf: not before its startup
// check, not after a check whose signing failed, and only after one signs.
func TestReadyOnlyAfterASign(t *testing.T) {
	s := &flakySigner{}
	iss := newTestIssuerSigner(t, 6*time.Hour, func(k *rsa.PrivateKey) crypto.Signer { s.Signer = k; return s })
	hosts := []string{"api.anthropic.com"}
	var r readiness
	if c := readyStatus(&r); c != http.StatusServiceUnavailable {
		t.Errorf("before any sign: %d, want 503", c)
	}
	s.fail.Store(true)
	if _, err := checkSigning(iss, hosts, false, &r); err == nil {
		t.Fatal("the check passed with a signer that can't sign")
	}
	if c := readyStatus(&r); c != http.StatusServiceUnavailable {
		t.Errorf("after a failed sign: %d, want 503", c)
	}
	s.fail.Store(false)
	if _, err := checkSigning(iss, hosts, false, &r); err != nil {
		t.Fatal(err)
	}
	if c := readyStatus(&r); c != http.StatusOK {
		t.Errorf("after a sign: %d, want 200", c)
	}
}
