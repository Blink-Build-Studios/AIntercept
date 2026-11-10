package events

import (
	"encoding/json"
	"strings"
)

// accountPaths are Claude Code's account endpoints: the only traffic that names the
// signed-in person (its OAuth token is opaque; analytics carry no email or name).
var accountPaths = []string{"/api/oauth/profile", "/api/claude_cli/bootstrap"}

// AccountFromResponse reads the signed-in account from a Claude Code account
// endpoint's response (JSON, decoded per its Content-Encoding): /api/oauth/profile names the account (email,
// full and display name) and its organization; /api/claude_cli/bootstrap names its
// email and organization. Nil for any other path or an unreadable body. Read in
// memory like the usage parse, so it works without body capture.
func AccountFromResponse(path string, body []byte, encoding string) *AuthIdentity {
	p := path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	match := false
	for _, a := range accountPaths {
		match = match || p == a
	}
	if !match || len(body) == 0 {
		return nil
	}
	body, err := decodeBody(body, encoding)
	if err != nil {
		return nil
	}
	var v struct {
		Account *struct {
			UUID        string `json:"uuid"`
			Email       string `json:"email"`
			FullName    string `json:"full_name"`
			DisplayName string `json:"display_name"`
		} `json:"account"`
		Organization *struct {
			Name string `json:"name"`
		} `json:"organization"`
		OAuthAccount *struct {
			AccountUUID      string `json:"account_uuid"`
			AccountEmail     string `json:"account_email"`
			DisplayName      string `json:"display_name"`
			OrganizationName string `json:"organization_name"`
		} `json:"oauth_account"`
	}
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	id := &AuthIdentity{}
	if a := v.Account; a != nil {
		id.AccountID, id.Email = a.UUID, a.Email
		id.Name = a.DisplayName
		if a.FullName != "" {
			id.Name = a.FullName
		}
	}
	if v.Organization != nil {
		id.Org = v.Organization.Name
	}
	if o := v.OAuthAccount; o != nil {
		id.AccountID = firstNonEmpty(id.AccountID, o.AccountUUID)
		id.Email = firstNonEmpty(id.Email, o.AccountEmail)
		id.Name = firstNonEmpty(id.Name, o.DisplayName)
		id.Org = firstNonEmpty(id.Org, o.OrganizationName)
	}
	if id.AccountID == "" || (id.Email == "" && id.Name == "") {
		return nil
	}
	return id
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
