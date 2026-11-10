package ca

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"
)

// SelfCheck mints a throwaway leaf for each host and verifies it against the
// configured chain exactly as a client would, before certsvc serves anything.
// Go's verifier enforces name constraints on every CA in the chain, so an
// allowlisted host the intermediate is not permitted to sign for fails here
// at startup, instead of as a TLS error on every laptop. It also catches an
// expired intermediate or a chain that doesn't link up.
//
// The last certificate in the chain is the trust anchor (the root, when the
// chain includes it). It reports whether any CA in the chain carries DNS name
// constraints; with requireConstraints, an unconstrained chain is an error.
func (i *Issuer) SelfCheck(hosts []string, requireConstraints bool) (constrained bool, err error) {
	chain, err := parseChain(i.chainPEM)
	if err != nil {
		return false, err
	}
	for _, c := range chain {
		// Only a permitted list limits what the CA can sign for; excluded
		// names alone (e.g. "excluded: .mil") still allow nearly anything.
		if len(c.PermittedDNSDomains) > 0 {
			constrained = true
		}
	}
	if requireConstraints && !constrained {
		return false, fmt.Errorf("no CA in the chain carries permitted DNS name constraints; the issuing intermediate must be name-constrained to the routed domains")
	}

	roots := x509.NewCertPool()
	roots.AddCert(chain[len(chain)-1])
	inters := x509.NewCertPool()
	for _, c := range chain[:len(chain)-1] {
		inters.AddCert(c)
	}

	for _, host := range hosts {
		i.mu.Lock()
		leaf, err := i.mint(host)
		i.mu.Unlock()
		if err != nil {
			return constrained, fmt.Errorf("%s: %w", host, err)
		}
		block, _ := pem.Decode(leaf.CertChainPEM)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return constrained, fmt.Errorf("%s: parse minted leaf: %w", host, err)
		}
		_, err = cert.Verify(x509.VerifyOptions{
			DNSName:       host,
			Roots:         roots,
			Intermediates: inters,
			CurrentTime:   time.Now(),
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return constrained, fmt.Errorf("%s: a leaf for this host would be rejected by clients: %w", host, err)
		}
	}
	return constrained, nil
}

func parseChain(pemBytes []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for rest := pemBytes; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse chain: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("chain has no certificates")
	}
	return out, nil
}
