package probe

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// proxyStub stands in for Envoy: a TLS server answering for any SNI.
func proxyStub(t *testing.T, h http.HandlerFunc) (*Prober, func()) {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return &Prober{
		Target:          strings.TrimPrefix(srv.URL, "https://"),
		Roots:           roots,
		MinLeafValidity: time.Minute,
		Timeout:         5 * time.Second,
	}, srv.Close
}

// httptest's certificate is valid for "example.com".
const host = "example.com"

func TestCheckProviderAnswer(t *testing.T) {
	p, done := proxyStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "cloudflare")
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer done()
	r := p.Check(context.Background(), host, "")
	if !r.OK || r.Status != 401 {
		t.Fatalf("want OK with status 401, got %+v", r)
	}
	if r.LeafNotAfter.IsZero() {
		t.Fatal("want the served leaf's expiry recorded")
	}
}

func TestCheckEnvoyLocalReplyFails(t *testing.T) {
	p, done := proxyStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Server"] = nil
		http.Error(w, "upstream connect error or disconnect/reset before headers", http.StatusServiceUnavailable)
	})
	defer done()
	if r := p.Check(context.Background(), host, ""); r.OK || !strings.Contains(r.Error, "wasn't reached") {
		t.Fatalf("want upstream failure, got %+v", r)
	}
}

func TestCheckEnvoyLocal404Fails(t *testing.T) {
	// Envoy's own 404 (no route for the host) also has no server header.
	p, done := proxyStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Server"] = nil
		w.WriteHeader(http.StatusNotFound)
	})
	defer done()
	if r := p.Check(context.Background(), host, ""); r.OK || !strings.Contains(r.Error, "wasn't reached") {
		t.Fatalf("want an Envoy-local 404 to fail, got %+v", r)
	}
}

func TestCheckUntrustedCertFails(t *testing.T) {
	p, done := proxyStub(t, func(w http.ResponseWriter, _ *http.Request) {})
	defer done()
	p.Roots = x509.NewCertPool()
	if r := p.Check(context.Background(), host, ""); r.OK {
		t.Fatalf("want TLS failure against an unrelated root, got %+v", r)
	}
}

func TestCheckLeafNearExpiryFails(t *testing.T) {
	p, done := proxyStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "x")
	})
	defer done()
	p.MinLeafValidity = 1000000 * time.Hour
	if r := p.Check(context.Background(), host, ""); r.OK || !strings.Contains(r.Error, "renewal") {
		t.Fatalf("want leaf-validity failure, got %+v", r)
	}
}

func TestMonitorServesVerdict(t *testing.T) {
	status := http.StatusUnauthorized
	p, done := proxyStub(t, func(w http.ResponseWriter, _ *http.Request) {
		if status >= 500 {
			w.Header()["Server"] = nil
		} else {
			w.Header().Set("Server", "x")
		}
		w.WriteHeader(status)
	})
	defer done()
	m := &Monitor{Prober: p, Targets: []Target{{Host: host}}, Interval: time.Hour}

	get := func() int {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/path", nil))
		return rec.Code
	}
	if got := get(); got != http.StatusServiceUnavailable {
		t.Fatalf("before the first round: want 503, got %d", got)
	}
	m.round(context.Background())
	if got := get(); got != http.StatusOK {
		t.Fatalf("healthy path: want 200, got %d", got)
	}
	status = http.StatusServiceUnavailable
	m.round(context.Background())
	if got := get(); got != http.StatusServiceUnavailable {
		t.Fatalf("Envoy local reply: want 503, got %d", got)
	}
}

func TestProbeSendsMarker(t *testing.T) {
	var got string
	p, done := proxyStub(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(MarkerHeader)
		w.Header().Set("Server", "x")
	})
	defer done()
	p.Marker = Marker("salt")
	p.Check(context.Background(), host, "")
	if got == "" || got != Marker("salt") {
		t.Fatalf("probe request carried marker %q, want %q", got, Marker("salt"))
	}
	if Marker("salt") == Marker("other") {
		t.Fatal("marker must depend on the salt")
	}
}

func TestParseTargets(t *testing.T) {
	got := ParseTargets(
		[]string{"api.anthropic.com", "claude.ai", "*.oaiusercontent.com", "*.other.com"},
		[]string{"claude.ai=claude.ai/cdn-cgi/trace", "*.oaiusercontent.com=sdmntprcentralus.oaiusercontent.com/"},
	)
	want := []Target{
		{Entry: "api.anthropic.com", Host: "api.anthropic.com", Path: "/"},
		{Entry: "claude.ai", Host: "claude.ai", Path: "/cdn-cgi/trace"},
		{Entry: "*.oaiusercontent.com", Host: "sdmntprcentralus.oaiusercontent.com", Path: "/"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("target %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}
