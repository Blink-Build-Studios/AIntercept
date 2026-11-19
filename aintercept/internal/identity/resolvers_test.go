package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Fake credentials: shaped like the real ones, never real.
const (
	oauthTok  = "sk-ant-oat01-FAKEoauthTOKENfortests0001"
	apiKey    = "sk-ant-api03-FAKEapiKEYfortests000000000001"
	openaiKey = "sk-proj-FAKEopenaiKEYfortests0001"
)

// jwt is an unsigned JWT with claims.
func jwt(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".FAKEsig"
}

var codexTok = jwt(map[string]any{
	"https://api.openai.com/profile": map[string]any{"email": "dev@example.com"},
	"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"},
	"name":                           "Dev Example",
})

// fake serves body at path for requests carrying want in header; anything
// else gets 401. status overrides 200 when set.
func fake(t *testing.T, path, header, want string, status int, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(header) != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

type lookupCase struct {
	name     string
	r        Resolver
	path     string
	header   string
	want     string // the header's value the provider expects
	token    string
	okBody   string
	check    func(t *testing.T, o *Owner)
	badShape string
}

var lookupCases = []lookupCase{
	{
		name: "anthropic oauth", r: anthropicOAuth{}, path: "/api/oauth/profile", header: "Authorization", want: "Bearer " + oauthTok, token: oauthTok,
		okBody: `{"account":{"uuid":"acc-1","email":"a@example.com","full_name":"Ada Example","display_name":"Ada"},"organization":{"uuid":"org-1","name":"Example Co"},"extra":{"ignored":true}}`,
		check: func(t *testing.T, o *Owner) {
			if *o != (Owner{AccountID: "acc-1", Email: "a@example.com", Name: "Ada Example", OrgID: "org-1", Org: "Example Co"}) {
				t.Errorf("owner %+v", *o)
			}
		},
		badShape: `{"account":{"uuid":"acc-1"}}`,
	},
	{
		name: "anthropic api key", r: anthropicKey{}, path: "/v1/organizations/me", header: "x-api-key", want: apiKey, token: apiKey,
		okBody: `{"id":"org-1","type":"organization","name":"Example Co"}`,
		check: func(t *testing.T, o *Owner) {
			if *o != (Owner{OrgID: "org-1", Org: "Example Co"}) || o.Person() {
				t.Errorf("owner %+v: want the org only", *o)
			}
		},
		badShape: `[1,2,3]`,
	},
	{
		name: "openai api key", r: openAIKey{}, path: "/v1/me", header: "Authorization", want: "Bearer " + openaiKey, token: openaiKey,
		okBody: `{"object":"user","id":"user-1","email":"o@example.com","name":"Olive Example","orgs":{"object":"list","data":[{"id":"org-a","title":"Personal","is_default":false},{"id":"org-b","title":"Example Co","is_default":true}]}}`,
		check: func(t *testing.T, o *Owner) {
			if *o != (Owner{AccountID: "user-1", Email: "o@example.com", Name: "Olive Example", OrgID: "org-b", Org: "Example Co"}) {
				t.Errorf("owner %+v", *o)
			}
		},
		badShape: `"a string"`,
	},
	{
		name: "chatgpt jwt", r: chatGPT{}, path: "/backend-api/wham/accounts/check", header: "Authorization", want: "Bearer " + codexTok, token: codexTok,
		okBody: `{"accounts":[{"id":"acct-0","name":"Other"},{"id":"acct-1","name":"Example Team","plan_type":"team"}],"account_ordering":["acct-1"]}`,
		check: func(t *testing.T, o *Owner) {
			if *o != (Owner{AccountID: "acct-1", Email: "dev@example.com", Name: "Dev Example", Org: "Example Team"}) {
				t.Errorf("owner %+v", *o)
			}
		},
		badShape: `{"accounts":"nope"}`,
	},
}

// Every resolver's lookup: success, 401, 403, an unexpected shape, a timeout.
func TestLookups(t *testing.T) {
	for _, tc := range lookupCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := fake(t, tc.path, tc.header, tc.want, 0, tc.okBody)
			o, err := tc.r.Lookup(ctx, s.Client(), s.URL, tc.token)
			if err != nil || o == nil {
				t.Fatalf("success: %v, %v", o, err)
			}
			tc.check(t, o)

			for _, code := range []int{401, 403} {
				s := fake(t, tc.path, tc.header, tc.want, code, "{}")
				o, err := tc.r.Lookup(ctx, s.Client(), s.URL, tc.token)
				var se *StatusError
				if o != nil || !errors.As(err, &se) || se.Code != code {
					t.Errorf("%d: %v, %v", code, o, err)
				}
				if want := map[int]string{401: "unauthorized", 403: "forbidden"}[code]; result(o, err) != want {
					t.Errorf("%d: result %q", code, result(o, err))
				}
			}

			s = fake(t, tc.path, tc.header, tc.want, 0, tc.badShape)
			if o, err := tc.r.Lookup(ctx, s.Client(), s.URL, tc.token); o != nil || result(o, err) != "parse" {
				t.Errorf("bad shape: %+v, %v", o, err)
			}

			slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}))
			defer slow.Close()
			tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			if o, err := tc.r.Lookup(tctx, slow.Client(), slow.URL, tc.token); o != nil || result(o, err) != "timeout" {
				t.Errorf("timeout: %+v, %v (%s)", o, err, result(o, err))
			}
		})
	}
}

// ChatGPT's accounts/check also comes as a map of id to account; and a
// token that doesn't reach its claims' account confirms nothing.
func TestChatGPTAccountsShapes(t *testing.T) {
	ctx := context.Background()
	m := fake(t, "/backend-api/wham/accounts/check", "Authorization", "Bearer "+codexTok, 0,
		`{"accounts":{"acct-1":{"account":{"account_id":"acct-1","name":"Example Team"}}},"account_ordering":["acct-1"]}`)
	if o, err := (chatGPT{}).Lookup(ctx, m.Client(), m.URL, codexTok); err != nil || o == nil || o.AccountID != "acct-1" || o.Email != "dev@example.com" {
		t.Errorf("map shape: %+v, %v", o, err)
	}
	other := fake(t, "/backend-api/wham/accounts/check", "Authorization", "Bearer "+codexTok, 0, `{"accounts":[{"id":"acct-9"}]}`)
	if o, err := (chatGPT{}).Lookup(ctx, other.Client(), other.URL, codexTok); err != nil || o != nil {
		t.Errorf("another account: %+v, %v; want nothing confirmed", o, err)
	}
	gone := fake(t, "/backend-api/wham/accounts/check", "Authorization", "Bearer "+codexTok, 0, `{"accounts":[{"id":"acct-1","is_deactivated":true}]}`)
	if o, _ := (chatGPT{}).Lookup(ctx, gone.Client(), gone.URL, codexTok); o != nil {
		t.Errorf("deactivated: %+v", o)
	}
}

// Which resolver takes which credential, by host and shape.
func TestEligibility(t *testing.T) {
	c := New(Config{Lookup: true})
	for _, tc := range []struct {
		host, header, value, want string
	}{
		{"api.anthropic.com", "authorization", "Bearer " + oauthTok, "anthropic"},
		{"api.anthropic.com:443", "authorization", "Bearer " + oauthTok, "anthropic"},
		{"API.Anthropic.com", "x-api-key", apiKey, "anthropic"},
		{"api.anthropic.com", "x-api-key", "sk-ant-other-FAKE", ""},
		{"api.openai.com", "authorization", "Bearer " + openaiKey, "openai"},
		{"api.openai.com", "authorization", "Bearer " + codexTok, ""},
		{"chatgpt.com", "authorization", "Bearer " + codexTok, "chatgpt"},
		{"chatgpt.com", "authorization", "Bearer " + openaiKey, ""},
		{"api.deepseek.com", "authorization", "Bearer " + openaiKey, ""},
		{"evil.example", "authorization", "Bearer " + oauthTok, ""},
		{"generativelanguage.googleapis.com", "x-goog-api-key", "AIzaFAKEgeminiKEY", ""},
	} {
		r, tok := c.resolver(tc.host, map[string]string{tc.header: tc.value})
		got := ""
		if r != nil {
			got = r.Provider()
			if tok == "" {
				t.Errorf("%s: no token", tc.host)
			}
		}
		if got != tc.want {
			t.Errorf("%s %s: resolver %q, want %q", tc.host, tc.value, got, tc.want)
		}
	}
	only := New(Config{Lookup: true, Providers: []string{"openai"}})
	if r, _ := only.resolver("api.anthropic.com", map[string]string{"authorization": "Bearer " + oauthTok}); r != nil {
		t.Errorf("providers [openai] took an Anthropic token: %s", r.Provider())
	}
}

// recorder is a transport that records the URLs asked for and answers 401.
type recorder struct{ urls []string }

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.urls = append(r.urls, req.URL.String())
	return &http.Response{StatusCode: 401, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

// The lookup's target is fixed in the resolver: Host, :authority and
// X-Forwarded-Host don't move it.
func TestLookupTargetIsFixed(t *testing.T) {
	rec := &recorder{}
	c := New(Config{Lookup: true, Client: &http.Client{Transport: rec}})
	p := c.Begin("kh-1", "sk-ant…0001", "api.anthropic.com", map[string]string{
		"authorization": "Bearer " + oauthTok, ":authority": "evil.example", "host": "evil.example",
		"x-forwarded-host": "evil.example", ":path": "https://evil.example/x",
	})
	if !p.Wait(time.Second) {
		t.Fatal("lookup didn't end")
	}
	if len(rec.urls) != 1 || rec.urls[0] != "https://api.anthropic.com/api/oauth/profile" {
		t.Errorf("asked %v", rec.urls)
	}
}
