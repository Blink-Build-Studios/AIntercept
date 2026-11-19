// Package identity names the person behind the credential an intercepted call
// carries (docs/PROVIDERS.md, "Identity"), for the Interceptor's Identity and
// the credentials table.
//
// Each provider has a Resolver: which credentials it can look up, and how (a
// URL fixed in its code, never taken from the request). The first time a pod
// sees a credential, the Cache asks the provider once, with the caller's own
// token, at the request's headers, off the request path; it also learns from
// the provider's account responses that pass through the proxy (Observe). It
// keeps each answer (24 h), and each refusal (1 h) or failure (60 s), keyed
// by the salted key hash: never the token. A call's identity is resolved from
// what the cache knows (Resolve): the credential's verified owner, else the
// token's claims, else the account the client reports.
//
// Scaling budget: one map read per call; one HTTPS request per new
// credential per pod per TTL, bounded by MaxInFlight and a rate limit; one
// small entry per live credential per pod, LRU-capped. Nothing is shared
// between pods.
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Source is how an identity's email and name were established (the
// Interceptor's IdentitySource, value for value).
type Source int32

const (
	SourceNone       Source = iota // not known
	SourceCredential               // the provider confirmed them for this credential
	SourceClaims                   // the bearer token's own claims, not verified
	SourceAccount                  // the account the client reported, seen before
)

func (s Source) String() string {
	return [...]string{"none", "credential", "claims", "account"}[s]
}

// Owner is who a credential belongs to, as one source tells it.
type Owner struct {
	// AccountID is the person's account at the provider; "" when only an
	// organization is known (an Anthropic API key without the directory).
	AccountID   string
	Email, Name string
	OrgID, Org  string
	// Incomplete marks an answer that may improve soon (a directory still
	// loading): it's kept ErrorTTL, not TTL.
	Incomplete bool
}

// Person reports whether the owner names a person, not only an organization.
func (o *Owner) Person() bool { return o.AccountID != "" || o.Email != "" }

// Resolver knows how one provider names the owner of its credentials.
type Resolver interface {
	// Provider names the resolver ("anthropic", "openai", "chatgpt"): what
	// Config.Providers lists.
	Provider() string
	// Credential is the token this resolver can look up for a request to
	// host with these (raw) headers, or ok false.
	Credential(host string, hdrs map[string]string) (token string, ok bool)
	// Origin is the provider's scheme and host, which Lookup's fixed path is
	// appended to ("https://api.anthropic.com").
	Origin() string
	// Lookup asks the provider who owns token, at origin plus a path fixed in
	// the resolver. nil, nil: the provider doesn't say. Errors never hold the
	// token or the response body.
	Lookup(ctx context.Context, c *http.Client, origin, token string) (*Owner, error)
}

// Observer is a Resolver that also reads the owner from the provider's own
// account responses passing through the proxy.
type Observer interface {
	// Observe is the owner a call's complete row names (after extraction),
	// or nil.
	Observe(e *events.Event) *Owner
}

// StatusError is a lookup the provider answered with a status other than 200.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("status %d", e.Code) }

// ErrShape is a 200 whose body isn't the shape expected.
var ErrShape = errors.New("unexpected response shape")

// result names a lookup's outcome, for the counters and the log.
func result(o *Owner, err error) string {
	var se *StatusError
	switch {
	case err == nil && o == nil:
		return "unknown"
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrShape):
		return "parse"
	case errors.As(err, &se) && se.Code == http.StatusUnauthorized:
		return "unauthorized"
	case errors.As(err, &se) && se.Code == http.StatusForbidden:
		return "forbidden"
	}
	return "error"
}

// Call is what one call says about who makes it.
type Call struct {
	KeyHash string
	// Reported is the account the client reports (the body's
	// metadata.user_id account, the chatgpt-account-id header, the token's
	// claim).
	Reported string
	// Claims are the bearer token's claims (nil: none).
	Claims *events.AuthIdentity
	// OrgID is the organization the request names.
	OrgID string
}

// Identity is who a call is from, resolved.
type Identity struct {
	Email, Name, OrgID string
	Source             Source
	// Reported is what the client sent; Verified what the provider confirmed
	// for the credential; Mismatch that they differ.
	Reported, Verified string
	Mismatch           bool
}

// Resolve is the call's identity from what's known now (never waits): the
// credential's confirmed owner, else the token's claims, else an account seen
// before under the id the client reports. An owner confirmed for the
// credential is never overridden by what the client sends. A nil Cache knows
// nothing but the call.
func (c *Cache) Resolve(q Call) Identity {
	id := Identity{Reported: q.Reported, OrgID: q.OrgID}
	if k, ok := c.Get(q.KeyHash); ok {
		if k.OrgID != "" {
			id.OrgID = k.OrgID
		}
		if k.Person() {
			id.Email, id.Name, id.Verified, id.Source = k.Email, k.Name, k.AccountID, SourceCredential
			id.Mismatch = id.Verified != "" && q.Reported != "" && q.Reported != id.Verified
			return id
		}
	}
	if a := q.Claims; a != nil && (a.Email != "" || a.Name != "") {
		id.Email, id.Name, id.Source = a.Email, a.Name, SourceClaims
		return id
	}
	if k, ok := c.ByAccount(q.Reported); ok && (k.Email != "" || k.Name != "") {
		id.Email, id.Name, id.Source = k.Email, k.Name, SourceAccount
	}
	return id
}

// hostOf is a host without its port, lowercased.
func hostOf(host string) string {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}
