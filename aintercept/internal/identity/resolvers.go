package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The providers' resolvers (docs/PROVIDERS.md, "Identity"):
//
//	credential                         lookup                                          gives
//	Anthropic OAuth (sk-ant-oat…)      GET api.anthropic.com/api/oauth/profile         person, org
//	Anthropic API key (sk-ant-api…)    GET api.anthropic.com/v1/organizations/me       org; person from the Directory
//	OpenAI API key (sk-…)              GET api.openai.com/v1/me                        person, default org
//	ChatGPT / Codex (JWT bearer)       GET chatgpt.com/backend-api/wham/accounts/check the claims' account, confirmed
//	Gemini API key                     none (a key belongs to a project, not a person)
//
// Each URL is fixed here: a request's Host never chooses where its token goes.

// Resolvers are the providers' resolvers, in the order they're tried. dir is
// the Anthropic admin directory (nil: off).
func Resolvers(dir *Directory) []Resolver {
	return []Resolver{anthropicOAuth{}, anthropicKey{dir: dir}, openAIKey{}, chatGPT{}}
}

const userAgent = "aintercept-identity"

// maxBody bounds a lookup's response.
const maxBody = 1 << 20

// getJSON GETs url and decodes a 200's JSON body into v. Its errors carry
// only the status (StatusError), a timeout, or ErrShape: never the request,
// so never the token.
func getJSON(ctx context.Context, c *http.Client, url string, hdrs map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("bad lookup URL")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	for k, val := range hdrs {
		req.Header.Set(k, val)
	}
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return errors.New("lookup request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &StatusError{Code: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return errors.New("lookup response failed")
	}
	if json.Unmarshal(b, v) != nil {
		return ErrShape
	}
	return nil
}

func isJWT(tok string) bool {
	return strings.HasPrefix(tok, "eyJ") && strings.Count(tok, ".") == 2
}

// ── Anthropic ────────────────────────────────────────────────────────────────

const anthropicHost = "api.anthropic.com"

// anthropicOAuth: Claude Code's OAuth token. The lookup is the client's own
// call (/api/oauth/profile); Observe reads that and /api/claude_cli/bootstrap
// when the client makes them.
type anthropicOAuth struct{}

func (anthropicOAuth) Provider() string { return "anthropic" }
func (anthropicOAuth) Origin() string   { return "https://" + anthropicHost }

func (anthropicOAuth) Credential(host string, hdrs map[string]string) (string, bool) {
	tok := events.APIKey(hdrs)
	return tok, hostOf(host) == anthropicHost && strings.HasPrefix(tok, "sk-ant-oat")
}

func (anthropicOAuth) Lookup(ctx context.Context, c *http.Client, origin, token string) (*Owner, error) {
	var body json.RawMessage
	if err := getJSON(ctx, c, origin+"/api/oauth/profile", map[string]string{
		"Authorization": "Bearer " + token, "anthropic-beta": "oauth-2025-04-20",
	}, &body); err != nil {
		return nil, err
	}
	a := events.AccountFromResponse("/api/oauth/profile", body, "")
	if a == nil {
		return nil, ErrShape
	}
	return &Owner{AccountID: a.AccountID, Email: a.Email, Name: a.Name, OrgID: a.OrgID, Org: a.Org}, nil
}

func (anthropicOAuth) Observe(e *events.Event) *Owner {
	a := e.Auth
	if a == nil || a.AccountID == "" || e.Status != http.StatusOK || hostOf(e.Host) != anthropicHost || !events.IsAccountPath(e.Path) {
		return nil
	}
	return &Owner{AccountID: a.AccountID, Email: a.Email, Name: a.Name, OrgID: a.OrgID, Org: a.Org}
}

// anthropicKey: an Anthropic API key. Its lookup names only the organization
// (the key belongs to it); the person who created the key comes from the
// admin directory, when the operator supplies an admin key.
type anthropicKey struct{ dir *Directory }

func (anthropicKey) Provider() string { return "anthropic" }
func (anthropicKey) Origin() string   { return "https://" + anthropicHost }

func (anthropicKey) Credential(host string, hdrs map[string]string) (string, bool) {
	tok := events.APIKey(hdrs)
	return tok, hostOf(host) == anthropicHost && strings.HasPrefix(tok, "sk-ant-api")
}

func (k anthropicKey) Lookup(ctx context.Context, c *http.Client, origin, token string) (*Owner, error) {
	var org struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := getJSON(ctx, c, origin+"/v1/organizations/me", map[string]string{
		"x-api-key": token, "anthropic-version": "2023-06-01",
	}, &org); err != nil {
		return nil, err
	}
	if org.ID == "" || org.Type != "" && org.Type != "organization" {
		return nil, ErrShape
	}
	o := &Owner{OrgID: org.ID, Org: org.Name}
	if k.dir != nil {
		p, ok, ready := k.dir.Match(token)
		switch {
		case ok:
			o.AccountID, o.Email, o.Name = p.AccountID, p.Email, p.Name
		case !ready:
			o.Incomplete = true
		}
	}
	return o, nil
}

// ── OpenAI ───────────────────────────────────────────────────────────────────

// openAIKey: an OpenAI API key. /v1/me names the key's user and their
// organizations (parsed tolerantly: an undocumented shape).
type openAIKey struct{}

func (openAIKey) Provider() string { return "openai" }
func (openAIKey) Origin() string   { return "https://api.openai.com" }

func (openAIKey) Credential(host string, hdrs map[string]string) (string, bool) {
	tok := events.APIKey(hdrs)
	return tok, hostOf(host) == "api.openai.com" && strings.HasPrefix(tok, "sk-") && !isJWT(tok)
}

func (openAIKey) Lookup(ctx context.Context, c *http.Client, origin, token string) (*Owner, error) {
	var me struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Orgs  struct {
			Data []struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				Name      string `json:"name"`
				IsDefault bool   `json:"is_default"`
			} `json:"data"`
		} `json:"orgs"`
	}
	if err := getJSON(ctx, c, origin+"/v1/me", map[string]string{"Authorization": "Bearer " + token}, &me); err != nil {
		return nil, err
	}
	if me.ID == "" && me.Email == "" {
		return nil, nil
	}
	o := &Owner{AccountID: me.ID, Email: me.Email, Name: me.Name}
	for i, org := range me.Orgs.Data {
		if org.IsDefault || i == 0 {
			o.OrgID, o.Org = org.ID, org.Title
			if o.Org == "" {
				o.Org = org.Name
			}
		}
		if org.IsDefault {
			break
		}
	}
	return o, nil
}

// chatGPT: a ChatGPT sign-in's JWT (Codex). Its claims name the person; the
// lookup is Codex's own accounts/check, which answers only a token ChatGPT
// accepts. When it lists the account the claims name, the claims are
// confirmed: the identity is the claims', now CREDENTIAL.
type chatGPT struct{}

func (chatGPT) Provider() string { return "chatgpt" }
func (chatGPT) Origin() string   { return "https://chatgpt.com" }

func (chatGPT) Credential(host string, hdrs map[string]string) (string, bool) {
	tok := events.APIKey(hdrs)
	return tok, hostOf(host) == "chatgpt.com" && isJWT(tok)
}

func (chatGPT) Lookup(ctx context.Context, c *http.Client, origin, token string) (*Owner, error) {
	claims := events.AuthIdentityFromHeaders(map[string]string{"authorization": "Bearer " + token})
	if claims == nil || claims.AccountID == "" || claims.Email == "" && claims.Name == "" {
		return nil, nil
	}
	var check struct {
		Accounts json.RawMessage `json:"accounts"`
	}
	if err := getJSON(ctx, c, origin+"/backend-api/wham/accounts/check", map[string]string{
		"Authorization": "Bearer " + token, "chatgpt-account-id": claims.AccountID,
	}, &check); err != nil {
		return nil, err
	}
	type account struct {
		ID            string `json:"id"`
		AccountID     string `json:"account_id"`
		Name          string `json:"name"`
		IsDeactivated bool   `json:"is_deactivated"`
	}
	var list []account
	if json.Unmarshal(check.Accounts, &list) != nil {
		// The other shape: a map of id to {account: {account_id, ...}}.
		var m map[string]struct {
			Account account `json:"account"`
		}
		if json.Unmarshal(check.Accounts, &m) != nil {
			return nil, ErrShape
		}
		for id, e := range m {
			a := e.Account
			if a.AccountID == "" {
				a.AccountID = id
			}
			list = append(list, a)
		}
	}
	for _, a := range list {
		if (a.ID == claims.AccountID || a.AccountID == claims.AccountID) && !a.IsDeactivated {
			return &Owner{AccountID: claims.AccountID, Email: claims.Email, Name: claims.Name, Org: a.Name}, nil
		}
	}
	return nil, nil // the token doesn't reach the account its claims name
}
