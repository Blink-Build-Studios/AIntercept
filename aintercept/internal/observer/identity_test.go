package observer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// What the pod learns becomes rows: a lookup's credential and account, an
// observed response's credential (its own row names the account), and
// nothing for an organization alone.
func TestIdentityRows(t *testing.T) {
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acc-1","email":"a@example.com","full_name":"Ada Example"},"organization":{"uuid":"org-1","name":"Example Co"}}`))
		case "/v1/organizations/me":
			_, _ = w.Write([]byte(`{"id":"org-1","type":"organization","name":"Example Co"}`))
		}
	}))
	defer p.Close()
	t.Setenv("OBSERVER_IDENTITY_TEST_ORIGIN", p.URL)
	cfg := identityFromEnv()
	if !cfg.cache.Lookup || cfg.wait != 300*time.Millisecond || cfg.enforceWait != 300*time.Millisecond || cfg.cache.TTL != 24*time.Hour {
		t.Fatalf("defaults %+v", cfg)
	}
	var mu sync.Mutex
	var rows []events.Event
	ids := setupIdentity(context.Background(), cfg, "pod-1", func(e events.Event) { mu.Lock(); rows = append(rows, e); mu.Unlock() })
	ids.Begin("kh-oauth", "sk-ant…0001", "api.anthropic.com", map[string]string{"authorization": "Bearer sk-ant-oat01-FAKE"}).Wait(2 * time.Second)
	ids.Begin("kh-key", "sk-ant…0002", "api.anthropic.com", map[string]string{"x-api-key": "sk-ant-api03-FAKE"}).Wait(2 * time.Second)
	ids.Observe(&events.Event{Host: "api.anthropic.com", Path: "/api/claude_cli/bootstrap", Status: 200, KeyHash: "kh-seen", KeyHint: "sk-ant…0003",
		Auth: &events.AuthIdentity{AccountID: "acc-2", Email: "b@example.com"}})
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		n := len(rows)
		mu.Unlock()
		if n >= 2 {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rows) != 2 {
		t.Fatalf("rows %+v, want two", rows)
	}
	l, o := rows[0], rows[1]
	if l.Phase != events.PhaseCredential || l.KeyHash != "kh-oauth" || l.KeyHint != "sk-ant…0001" || l.ObserverInstance != "pod-1" ||
		*l.Credential != (events.Credential{AccountID: "acc-1", Source: "lookup"}) || l.Auth == nil || l.Auth.Email != "a@example.com" || l.Auth.Org != "Example Co" {
		t.Errorf("lookup row %+v", l)
	}
	if o.KeyHash != "kh-seen" || *o.Credential != (events.Credential{AccountID: "acc-2", Source: "observed"}) || o.Auth != nil {
		t.Errorf("observed row %+v", o)
	}
}
