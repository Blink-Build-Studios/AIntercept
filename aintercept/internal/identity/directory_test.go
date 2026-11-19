package identity

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const adminKey = "sk-ant-admin01-FAKEadminKEYfortests0001"

// The keys the fake Admin API lists (hints as the API writes them: the key's
// first 16 characters, "...", its last 4) and its users, over two pages.
const (
	keyAda    = "sk-ant-api03-ADAxxxxxxxxxxxxxxxxxxxxxxxxxx1111"
	keyTwinA  = "sk-ant-api03-TWNaaaaaaaaaaaaaaaaaaaaaaaaaaa2222"
	keyLegacy = "sk-ant-api03-LEGxxxxxxxxxxxxxxxxxxxxxxxxxxx3333"
	keyBot    = "sk-ant-api03-BOTxxxxxxxxxxxxxxxxxxxxxxxxxxx4444"
)

func adminAPI(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	pages := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != adminKey || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		pages++
		switch r.URL.Path {
		case "/v1/organizations/users":
			if r.URL.Query().Get("after_id") == "" {
				_, _ = w.Write([]byte(`{"data":[{"id":"user-ada","type":"user","email":"ada@example.com","name":"Ada Example","role":"developer"}],"has_more":true,"last_id":"user-ada"}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"user-bo","type":"user","email":"bo@example.com","name":"Bo Example"}],"has_more":false,"last_id":"user-bo"}`))
		case "/v1/organizations/api_keys":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"k1","type":"api_key","status":"active","partial_key_hint":"sk-ant-api03-ADA...1111","created_by":{"id":"user-ada","type":"user"}},
				{"id":"k2","type":"api_key","status":"active","partial_key_hint":"sk-ant-api03-TWN...2222","created_by":{"id":"user-ada","type":"user"}},
				{"id":"k3","type":"api_key","status":"active","partial_key_hint":"sk-ant-api03-TWN...2222","created_by":{"id":"user-bo","type":"user"}},
				{"id":"k4","type":"api_key","status":"active","partial_key_hint":"sk-ant-api03-LEG...3333","created_by":null},
				{"id":"k5","type":"api_key","status":"active","partial_key_hint":"sk-ant-api03-BOT...4444","created_by":{"id":"sa-1","type":"service_account"}},
				{"id":"k6","type":"api_key","status":"archived","partial_key_hint":"sk-ant-api03-ADA...1111","created_by":{"id":"user-bo","type":"user"}}
			],"has_more":false,"last_id":"k6"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s, &pages
}

func TestDirectory(t *testing.T) {
	s, pages := adminAPI(t)
	d := &Directory{AdminKey: adminKey, Origin: s.URL, Client: s.Client()}
	if _, ok, ready := d.Match(keyAda); ok || ready {
		t.Fatal("matched, or ready, before any listing")
	}
	d.Refresh(context.Background())
	if *pages != 3 {
		t.Errorf("%d pages fetched, want 3 (users over two, keys)", *pages)
	}
	// A match: the key's creator (the archived key with the same hint isn't counted).
	if o, ok, ready := d.Match(keyAda); !ok || !ready || o != (Owner{AccountID: "user-ada", Email: "ada@example.com", Name: "Ada Example"}) {
		t.Errorf("match: %+v %v %v", o, ok, ready)
	}
	for name, key := range map[string]string{
		"two keys share the hint": keyTwinA,
		"created_by null":         keyLegacy,
		"a service account":       keyBot,
		"no key with the hint":    "sk-ant-api03-ZZZxxxxxxxxxxxxxxxxxxxxxxxxxxx9999",
	} {
		if o, ok, ready := d.Match(key); ok || !ready {
			t.Errorf("%s: attributed to %+v (ready %v)", name, o, ready)
		}
	}
}

// The Anthropic API key resolver adds the directory's person to the org; with
// no directory (no admin key) it gives the org only; before the directory's
// first listing, its answer is incomplete (kept briefly).
func TestAnthropicKeyWithDirectory(t *testing.T) {
	admin, _ := adminAPI(t)
	org := fake(t, "/v1/organizations/me", "x-api-key", keyAda, 0, `{"id":"org-1","type":"organization","name":"Example Co"}`)
	ctx := context.Background()
	d := &Directory{AdminKey: adminKey, Origin: admin.URL, Client: admin.Client()}

	o, err := anthropicKey{dir: d}.Lookup(ctx, org.Client(), org.URL, keyAda)
	if err != nil || !o.Incomplete || o.Person() {
		t.Errorf("before listing: %+v %v", o, err)
	}
	d.Refresh(ctx)
	o, err = anthropicKey{dir: d}.Lookup(ctx, org.Client(), org.URL, keyAda)
	if err != nil || *o != (Owner{AccountID: "user-ada", Email: "ada@example.com", Name: "Ada Example", OrgID: "org-1", Org: "Example Co"}) {
		t.Errorf("with the directory: %+v %v", o, err)
	}
	o, err = anthropicKey{}.Lookup(ctx, org.Client(), org.URL, keyAda)
	if err != nil || o.Person() || o.Incomplete || o.OrgID != "org-1" {
		t.Errorf("off: %+v %v", o, err)
	}
}

// A failed listing (a wrong admin key) is ready, attributes no one, and never
// logs the admin key.
func TestDirectoryFailureAndAdminKeyNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	s, _ := adminAPI(t)
	d := &Directory{AdminKey: adminKey + "-wrong", Origin: s.URL, Client: s.Client(), Every: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	waitFor(t, func() bool { _, _, ready := d.Match(keyAda); return ready })
	cancel()
	<-done
	if _, ok, _ := d.Match(keyAda); ok {
		t.Error("attributed with a failed listing")
	}
	if !strings.Contains(logs.String(), "unauthorized") {
		t.Errorf("the failure wasn't logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "FAKEadmin") {
		t.Errorf("the admin key is in the logs: %s", logs.String())
	}
}
