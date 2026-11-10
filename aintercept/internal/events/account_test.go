package events

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestAccountFromResponse(t *testing.T) {
	profile := []byte(`{"account":{"uuid":"acc-1","email":"a@example.com","full_name":"Ada Lovelace","display_name":"Ada"},"organization":{"uuid":"org-1","name":"Example Co"}}`)
	got := AccountFromResponse("/api/oauth/profile", profile, "")
	if got == nil || got.AccountID != "acc-1" || got.Email != "a@example.com" || got.Name != "Ada Lovelace" || got.Org != "Example Co" {
		t.Fatalf("profile: %+v", got)
	}
	boot := []byte(`{"oauth_account":{"account_uuid":"acc-1","account_email":"a@example.com","organization_name":"Example Co"}}`)
	got = AccountFromResponse("/api/claude_cli/bootstrap?x=1", boot, "")
	if got == nil || got.AccountID != "acc-1" || got.Email != "a@example.com" || got.Name != "" {
		t.Fatalf("bootstrap: %+v", got)
	}
	// Claude Code asks for compressed responses: a gzip-encoded profile still names the account.
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(profile)
	_ = w.Close()
	if got = AccountFromResponse("/api/oauth/profile", gz.Bytes(), "gzip"); got == nil || got.Email != "a@example.com" {
		t.Fatalf("gzip profile: %+v", got)
	}
	if AccountFromResponse("/v1/messages", profile, "") != nil {
		t.Fatal("other paths name no one")
	}
	if AccountFromResponse("/api/oauth/profile", []byte(`{"account":{"uuid":"acc-1"}}`), "") != nil {
		t.Fatal("an account with neither email nor name isn't an identity")
	}
}
