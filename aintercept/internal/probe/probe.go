// Package probe checks the interception path end to end, the way a laptop
// uses it: a TLS handshake to the proxy with the provider's hostname as SNI,
// verified against the interception root, then one HTTP request that must be
// answered by the real provider. It is what an automatic kill switch (or any
// monitor) polls to decide whether the proxy can carry traffic.
package probe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Target is one probe: the allowlist entry it covers, and the hostname and path
// to request. Path defaults to "/". A wildcard entry ("*.example.com") names a
// concrete host under it to probe.
type Target struct {
	Entry string `json:"entry,omitempty"`
	Host  string `json:"host"`
	Path  string `json:"path,omitempty"`
}

// ParseTargets builds probe targets from allowlist entries and overrides of the
// form "entry=host/path" (e.g. "claude.ai=claude.ai/cdn-cgi/trace" or
// "*.oaiusercontent.com=sdmntprcentralus.oaiusercontent.com/"). Wildcard
// entries without an override are skipped: they name no single host.
func ParseTargets(entries, overrides []string) []Target {
	ov := map[string]Target{}
	for _, o := range overrides {
		entry, url, ok := strings.Cut(o, "=")
		if !ok {
			continue
		}
		host, path, _ := strings.Cut(strings.TrimSpace(url), "/")
		ov[strings.TrimSpace(entry)] = Target{Host: host, Path: "/" + path}
	}
	var out []Target
	for _, e := range entries {
		t, ok := ov[e]
		switch {
		case ok:
		case strings.HasPrefix(e, "*."):
			continue
		default:
			t = Target{Host: e, Path: "/"}
		}
		t.Entry = e
		out = append(out, t)
	}
	return out
}

// Result is the outcome of probing one hostname.
type Result struct {
	Host string `json:"host"`
	Path string `json:"path,omitempty"`
	OK   bool   `json:"ok"`
	// Error says which stage failed: the TLS handshake (cert service or Envoy
	// listener), the leaf's remaining validity, or the upstream hop.
	Error        string    `json:"error,omitempty"`
	Status       int       `json:"status,omitempty"`
	LeafNotAfter time.Time `json:"leaf_not_after,omitempty"`
	LatencyMS    int64     `json:"latency_ms"`
}

// Prober probes hostnames through one proxy address.
type Prober struct {
	// Target is the proxy's host:port: the Envoy Service in-cluster, or the
	// load balancer's address to include the LB in the check.
	Target string
	// Roots holds the interception root the leaf must chain to.
	Roots *x509.CertPool
	// MinLeafValidity fails a host whose served leaf expires sooner than this,
	// catching a stalled cert renewal before clients see an expired cert.
	MinLeafValidity time.Duration
	Timeout         time.Duration
	// Marker is sent as MarkerHeader on every probe request so the observer can
	// tag the resulting events synthetic (see Marker).
	Marker string
}

// MarkerHeader carries the probe marker on probe requests.
const MarkerHeader = "x-aintercept-probe"

// Marker derives the probe marker from the deployment's key-hash salt, which
// every observer replica shares (a probe from one replica may be recorded by
// another). Clients don't know the salt, so they can't forge the marker to get
// their own traffic excluded from dashboards as synthetic.
func Marker(salt string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte("aintercept-probe-v1"))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// Check probes host once at path ("/" when empty).
func (p *Prober) Check(ctx context.Context, host, path string) (res Result) {
	start := time.Now()
	if path == "" {
		path = "/"
	}
	res.Host, res.Path = host, path
	defer func() { res.LatencyMS = time.Since(start).Milliseconds() }()

	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	dialer := &net.Dialer{}
	client := &http.Client{
		Transport: &http.Transport{
			// Whatever the URL says, connect to the proxy; SNI and Host stay the
			// provider's, exactly as for a DNS-steered client.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, p.Target)
			},
			TLSClientConfig:   &tls.Config{RootCAs: p.Roots, ServerName: host},
			ForceAttemptHTTP2: true,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "aintercept-proxyprobe")
	if p.Marker != "" {
		req.Header.Set(MarkerHeader, p.Marker)
	}
	resp, err := client.Do(req)
	if err != nil {
		res.Error = fmt.Sprintf("request through proxy: %v", err)
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	res.Status = resp.StatusCode

	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		res.LeafNotAfter = resp.TLS.PeerCertificates[0].NotAfter
		if left := time.Until(res.LeafNotAfter); left < p.MinLeafValidity {
			res.Error = fmt.Sprintf("served leaf expires in %s (minimum %s): cert renewal is stalled", left.Round(time.Second), p.MinLeafValidity)
			return res
		}
	}
	if IsLocalReply(resp) {
		res.Error = fmt.Sprintf("the provider wasn't reached: Envoy answered %d itself (no route, or upstream unreachable)", resp.StatusCode)
		return res
	}
	// Any response from the provider (401, 404, ...) proves the full path.
	res.OK = true
	return res
}

// IsLocalReply reports whether resp was generated by Envoy itself rather than
// relayed from the provider. Envoy forwards the provider's `server` header
// unchanged (PASS_THROUGH) and adds none to its own replies, while every
// provider response carries one; so a response without it, at any status
// (a 404 for an unrouted host, a 503 for an unreachable upstream), is Envoy's.
func IsLocalReply(resp *http.Response) bool {
	return strings.TrimSpace(resp.Header.Get("Server")) == ""
}
