// certsvc is an Envoy SDS (Secret Discovery Service) server that lazily mints
// short-lived leaf certificates for allowlisted SNI hostnames.
//
// Flow: Envoy's downstream TLS context references an SDS secret whose name is
// the SNI hostname. On each SDS DiscoveryRequest, this server checks the
// requested resource names against the allowlist, mints (or reuses a cached)
// leaf via the ca package, injects it into a LinearCache, and lets
// go-control-plane serve it back. Disallowed or empty names are refused — Envoy
// then fails the handshake closed.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	secretv3 "github.com/envoyproxy/go-control-plane/envoy/service/secret/v3"
	cachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	serverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"

	"github.com/Blink-Build-Studios/aintercept/certsvc/internal/ca"
)

func main() {
	addr := envDefault("CERTSVC_LISTEN", "0.0.0.0:18000")
	allowlistRaw := envDefault("CERT_ALLOWLIST", "api.anthropic.com")
	intCert := envDefault("INTERMEDIATE_CERT", "/certs/intermediate-ca.crt")
	intKey := envDefault("INTERMEDIATE_KEY", "/certs/intermediate-ca.key")
	chain := envDefault("CA_CHAIN", "/certs/ca-chain.crt")
	leafTTL := envDuration("LEAF_TTL", 6*time.Hour)
	renewScanInterval := envDuration("RENEW_SCAN_INTERVAL", time.Minute)

	allow := parseAllowlist(allowlistRaw)
	if len(allow.hosts()) == 0 {
		log.Fatal("certsvc: CERT_ALLOWLIST is empty; refusing to start (would mint for nothing)")
	}

	issuer, err := ca.NewIssuer(ca.Config{
		IntermediateCertPath: intCert,
		IntermediateKeyPath:  intKey,
		ChainPath:            chain,
		LeafTTL:              leafTTL,
	})
	if err != nil {
		log.Fatalf("certsvc: load issuer: %v", err)
	}

	cache := cachev3.NewLinearCache(resourcev3.SecretType)
	// Re-mint a cached leaf once it's within 1/4 of its TTL of expiry, so a leaf
	// never lapses mid-handshake but we also don't re-mint on every request.
	m := &minter{
		issuer:      issuer,
		cache:       cache,
		allow:       allow,
		renewBefore: leafTTL / 4,
		issued:      map[string]time.Time{},
	}

	callbacks := serverv3.CallbackFuncs{
		StreamRequestFunc: func(_ int64, req *discoveryv3.DiscoveryRequest) error {
			return m.ensure(req.GetResourceNames())
		},
		StreamDeltaRequestFunc: func(_ int64, req *discoveryv3.DeltaDiscoveryRequest) error {
			return m.ensure(req.GetResourceNamesSubscribe())
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := serverv3.NewServer(ctx, cache, callbacks)
	grpcServer := grpc.NewServer()
	secretv3.RegisterSecretDiscoveryServiceServer(grpcServer, srv)

	// Background renewer: re-mint leaves before they expire and push them to Envoy
	// over SDS (hot-swap, no dropped connections). Without this a steady host's
	// leaf would age out, since Envoy doesn't re-poll SDS after its initial fetch.
	go m.renewLoop(ctx, renewScanInterval)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("certsvc: listen %s: %v", addr, err)
	}
	log.Printf("certsvc: SDS listening on %s; allowlist=%v leafTTL=%s renewBefore=%s scanInterval=%s",
		addr, allow.hosts(), leafTTL, m.renewBefore, renewScanInterval)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("certsvc: serve: %v", err)
	}
}

// minter turns SDS resource-name requests into cached leaf secrets, enforcing
// the hostname allowlist (fail closed).
//
// Leaves are minted ONCE per host and cached. A naive implementation that
// re-mints on every SDS request produces fresh cert bytes each time, which bumps
// the LinearCache version on every request and makes Envoy re-subscribe in a
// hot loop (observed: ~500 mints/sec/pod at idle). We instead only write to the
// cache when there is no current leaf for the host or the existing one is within
// renewBefore of expiry — so the cached resource (and its version) stays stable
// and Envoy's SDS stream goes quiet after the initial push.
type minter struct {
	issuer *ca.Issuer
	cache  *cachev3.LinearCache
	allow  allowlist

	// renewBefore is how long before NotAfter a cached leaf is proactively
	// re-minted, so a leaf never expires out from under an active handshake.
	renewBefore time.Duration

	mu     sync.Mutex
	issued map[string]time.Time // host -> current leaf NotAfter
}

func (m *minter) ensure(names []string) error {
	for _, name := range names {
		host := strings.TrimSpace(name)
		if host == "" {
			// Envoy should always name the resource; an empty name is a config
			// or handshake problem. Fail closed.
			return fmt.Errorf("sds: empty secret/SNI name requested")
		}
		if !m.allow.permits(host) {
			log.Printf("certsvc: DENY secret request for non-allowlisted host %q (failing closed)", host)
			return fmt.Errorf("sds: host %q not in allowlist", host)
		}

		// Skip re-minting if we already have a leaf that isn't near expiry. This
		// keeps the cached resource byte-stable so Envoy doesn't churn.
		m.mu.Lock()
		notAfter, have := m.issued[host]
		fresh := have && time.Until(notAfter) > m.renewBefore
		m.mu.Unlock()
		if fresh {
			continue
		}

		if err := m.mintAndCache(host, false); err != nil {
			return fmt.Errorf("sds: %w", err)
		}
	}
	return nil
}

// mintAndCache mints a leaf for host, writes it into the SDS cache (which pushes
// it to any subscribed Envoy), and records its expiry. It is the single mint code
// path shared by the on-demand SDS request handler and the background renewer, so
// the two can never drift apart. When force is true it always mints a brand-new
// leaf (the renewer's path); otherwise the issuer may return a still-fresh cached
// leaf (the on-demand path, which avoids churn).
func (m *minter) mintAndCache(host string, force bool) error {
	mint := m.issuer.LeafFor
	if force {
		mint = m.issuer.ForceLeafFor
	}
	leaf, err := mint(host)
	if err != nil {
		return fmt.Errorf("mint leaf for %q: %w", host, err)
	}
	secret := &tlsv3.Secret{
		Name: host,
		Type: &tlsv3.Secret_TlsCertificate{
			TlsCertificate: &tlsv3.TlsCertificate{
				CertificateChain: inlineBytes(leaf.CertChainPEM),
				PrivateKey:       inlineBytes(leaf.PrivateKey),
			},
		},
	}
	// UpdateResource bumps the cached resource version; go-control-plane streams
	// the new secret to every subscribed Envoy, which hot-swaps it on live
	// listeners WITHOUT dropping connections. This is what lets a renewal take
	// effect with zero downtime.
	if err := m.cache.UpdateResource(host, secret); err != nil {
		return fmt.Errorf("cache update %q: %w", host, err)
	}
	m.mu.Lock()
	m.issued[host] = leaf.NotAfter
	m.mu.Unlock()
	log.Printf("certsvc: minted leaf for %q (valid until %s)", host, leaf.NotAfter.Format(time.RFC3339))
	return nil
}

// renewLoop proactively re-mints leaves BEFORE they expire. SDS is push-based:
// once Envoy subscribes and gets a secret, it does not poll again, so nothing on
// the request path re-fires for a steady host — a cached leaf would otherwise age
// past NotAfter and Envoy would keep serving an EXPIRED cert until a pod restart.
// This loop closes that gap: every scanInterval it re-mints any issued leaf
// within renewBefore of expiry and pushes it, so a live leaf is always renewed
// well ahead of expiry with no connection drop. Runs until ctx is cancelled.
func (m *minter) renewLoop(ctx context.Context, scanInterval time.Duration) {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.renewExpiring()
		}
	}
}

// renewExpiring re-mints every currently-issued leaf that is within renewBefore
// of its expiry. A mint failure is logged loudly and the loop continues to the
// next host — one host's CA hiccup must not stall renewal for the others, and the
// next scan retries.
func (m *minter) renewExpiring() {
	// Snapshot the hosts needing renewal under the lock, then mint outside it so a
	// slow CA op doesn't block SDS request handling.
	m.mu.Lock()
	var due []string
	for host, notAfter := range m.issued {
		if time.Until(notAfter) <= m.renewBefore {
			due = append(due, host)
		}
	}
	m.mu.Unlock()

	for _, host := range due {
		if err := m.mintAndCache(host, true); err != nil {
			log.Printf("ALARM certsvc: proactive renewal FAILED for %q: %v (will retry next scan)", host, err)
			continue
		}
		log.Printf("certsvc: proactively renewed leaf for %q ahead of expiry", host)
	}
}

func inlineBytes(b []byte) *corev3.DataSource {
	return &corev3.DataSource{Specifier: &corev3.DataSource_InlineBytes{InlineBytes: b}}
}

// Ensure the Secret satisfies the cache resource interface at compile time.
var _ cachetypes.Resource = (*tlsv3.Secret)(nil)

// allowlist supports exact hosts and leading-wildcard entries ("*.example.com").
type allowlist struct {
	exact    map[string]struct{}
	suffixes []string // for "*.example.com", stored as ".example.com"
}

func parseAllowlist(raw string) allowlist {
	a := allowlist{exact: map[string]struct{}{}}
	for _, part := range strings.Split(raw, ",") {
		h := strings.TrimSpace(strings.ToLower(part))
		if h == "" {
			continue
		}
		if strings.HasPrefix(h, "*.") {
			a.suffixes = append(a.suffixes, h[1:]) // ".example.com"
			continue
		}
		a.exact[h] = struct{}{}
	}
	return a
}

func (a allowlist) permits(host string) bool {
	h := strings.ToLower(host)
	if _, ok := a.exact[h]; ok {
		return true
	}
	for _, suf := range a.suffixes {
		if strings.HasSuffix(h, suf) && len(h) > len(suf) {
			return true
		}
	}
	return false
}

func (a allowlist) hosts() []string {
	out := make([]string, 0, len(a.exact)+len(a.suffixes))
	for h := range a.exact {
		out = append(out, h)
	}
	for _, s := range a.suffixes {
		out = append(out, "*"+s)
	}
	return out
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("certsvc: invalid %s=%q, using default %s", key, v, def)
	}
	return def
}
