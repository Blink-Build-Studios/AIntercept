package interceptor

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
)

// A call whose stream ends while it is still held for its identity (a short
// call) is marked ended, so its entry is swept when its final record never
// comes.
func TestEndedWhileHeldIsSwept(t *testing.T) {
	release := make(chan struct{})
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer p.Close()
	defer close(release)
	ids := identity.New(identity.Config{Lookup: true, Origin: p.URL, Client: p.Client()})
	now := time.Now()
	c := &Client{cfg: Config{QueueSize: 1024}, queues: []chan job{make(chan job, 4096)},
		held: map[string][]job{}, idents: map[string]*callIdentity{}, verdicts: map[string]*callVerdicts{},
		now: func() time.Time { return now }}
	lk := ids.Begin("kh", "", "api.anthropic.com", map[string]string{"authorization": "Bearer sk-ant-oat01-X"})
	c.ObserveCall(&CallInfo{CallID: "held", Host: "api.anthropic.com", KeyHash: "kh", Identities: ids, Lookup: lk}, 50*time.Millisecond)
	c.Ended("held") // a short call: its stream ends while it's still held
	c.awaiting.Wait()
	now = now.Add(20 * time.Minute)
	for i := range 600 {
		c.setIdentity("other-"+strconv.Itoa(i), nil)
	}
	if c.idents["held"] != nil {
		t.Fatalf("an ended call held for its identity is never swept: ended=%v", c.idents["held"].ended)
	}
}
