package observer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	ids, _ := setupIdentity(context.Background(), cfg, "pod-1", func(e events.Event) { mu.Lock(); rows = append(rows, e); mu.Unlock() })
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

// collect is a sink that keeps every row.
type collect struct {
	mu   sync.Mutex
	rows []events.Event
}

func (c *collect) Emit(e events.Event) error {
	c.mu.Lock()
	c.rows = append(c.rows, e)
	c.mu.Unlock()
	return nil
}
func (c *collect) Flush() error { return nil }

// Graceful shutdown with account responses still in the processor's buffer
// (each links an unseen credential, so each is learned while it drains) and a
// lookup in flight: nothing panics, the in-flight lookup's row is recorded,
// and the processor is closed last. The observer shuts down in this order.
func TestIdentityShutdown(t *testing.T) {
	release := make(chan struct{})
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"account":{"uuid":"acc-slow","email":"s@example.com"}}`))
	}))
	defer p.Close()
	t.Setenv("OBSERVER_IDENTITY_TEST_ORIGIN", p.URL)
	sink := &collect{}
	var proc *events.Processor
	ids, stop := setupIdentity(context.Background(), identityFromEnv(), "pod-1", func(e events.Event) { proc.Offer(e) })
	proc = events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 4096, OnComplete: func(e *events.Event) { ids.Observe(e) }})
	ids.Begin("kh-slow", "sk-ant…0009", "api.anthropic.com", map[string]string{"authorization": "Bearer sk-ant-oat01-FAKE"})
	for i := range 500 {
		proc.Offer(events.Event{Phase: events.PhaseComplete, Host: "api.anthropic.com", Path: "/api/oauth/profile", Status: 200,
			KeyHash: "kh-" + strconv.Itoa(i), Auth: &events.AuthIdentity{AccountID: "acc-" + strconv.Itoa(i), Email: "a@example.com"}})
	}
	go func() { time.Sleep(50 * time.Millisecond); close(release) }()
	stop()       // waits for the lookup in flight, records it, stops the rows
	proc.Close() // drains: what it learns now is dropped, never sent to a closed buffer
	proc.Offer(events.Event{Phase: events.PhaseVerdict})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	slow := false
	for _, e := range sink.rows {
		slow = slow || e.Phase == events.PhaseCredential && e.KeyHash == "kh-slow"
	}
	if !slow {
		t.Error("the in-flight lookup's credential row was lost")
	}
}
