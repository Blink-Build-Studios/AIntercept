package events

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

// AuthIdentity is who a request's bearer token says the caller is, read from
// the token's JWT claims. ChatGPT web and Codex sign in with an OpenAI access
// token whose claims carry the person's email and name, user and account ids,
// and plan. The token itself is never stored, and the claims aren't verified:
// this is attribution, not authorization.
type AuthIdentity struct {
	Email     string `json:"email,omitempty"`
	Name      string `json:"name,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Plan      string `json:"plan,omitempty"`
	// Org is the organization's name (Claude Code's account endpoints name it).
	Org       string `json:"org,omitempty"`
	Issuer    string `json:"issuer,omitempty"`
	Subject   string `json:"subject,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

// AuthIdentityFromHeaders decodes the claims of a JWT bearer token in the
// Authorization header, or returns nil when there is none (API keys, cookies).
func AuthIdentityFromHeaders(hdrs map[string]string) *AuthIdentity {
	parts := strings.Split(extractAPIKey(hdrs), ".")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "eyJ") {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var c map[string]any
	if json.Unmarshal(payload, &c) != nil {
		return nil
	}
	// Claims may be namespaced (OpenAI: "https://api.openai.com/profile"
	// .email, ".../auth".chatgpt_user_id); look in the top level, then in each
	// nested object in sorted key order, so a claim present in two of them
	// resolves the same way on every request.
	scopes := []map[string]any{c}
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m, ok := c[k].(map[string]any); ok {
			scopes = append(scopes, m)
		}
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			for _, s := range scopes {
				if v, ok := s[k].(string); ok && v != "" {
					return v
				}
			}
		}
		return ""
	}
	id := &AuthIdentity{
		Email:     str("email"),
		Name:      str("name"),
		UserID:    str("chatgpt_user_id", "user_id"),
		AccountID: str("chatgpt_account_id", "account_id"),
		Plan:      str("chatgpt_plan_type", "plan_type"),
		Issuer:    str("iss"),
		Subject:   str("sub"),
		ClientID:  str("client_id", "azp"),
		SessionID: str("session_id", "sid"),
	}
	if exp, ok := c["exp"].(float64); ok {
		id.ExpiresAt = int64(exp)
	}
	if *id == (AuthIdentity{}) {
		return nil
	}
	return id
}
