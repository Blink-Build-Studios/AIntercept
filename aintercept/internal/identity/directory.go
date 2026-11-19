package identity

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Directory attributes Anthropic API keys to the people who created them,
// from the organization's Admin API: each pod lists the org's keys (each with
// its creator and a partial hint of the key) and its users, every Every. A
// call's key is matched against the hints at its headers. Off unless the
// operator supplies an admin key; the admin key is never logged.
type Directory struct {
	// AdminKey is the organization's Admin API key.
	AdminKey string
	// Origin is the Admin API's (default https://api.anthropic.com; tests
	// set their own).
	Origin string
	// Every is how often the directory is listed again (default 15 min).
	Every  time.Duration
	Client *http.Client

	mu    sync.RWMutex
	keys  []dirKey
	ready bool // a listing has been tried
}

// dirKey is one active key: its hint's two ends, and its creator.
type dirKey struct {
	prefix, suffix string
	creator        *Owner // nil: not recorded, or not a person
}

// Run lists the directory now, then every Every, until ctx ends.
func (d *Directory) Run(ctx context.Context) {
	every := d.Every
	if every <= 0 {
		every = 15 * time.Minute
	}
	for {
		d.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Refresh lists the organization's keys and users once. A failed listing
// keeps the last one.
func (d *Directory) Refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	keys, err := d.list(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ready = true
	if err != nil {
		slog.Warn("identity directory: listing the Anthropic organization's API keys failed (the last listing stays)",
			"result", result(nil, err))
		return
	}
	d.keys = keys
	slog.Info("identity directory: Anthropic API keys listed", "keys", len(keys))
}

// Match is the person who created key, when exactly one active key's hint
// matches it and its creator is a known user. ready is false until the
// first listing has been tried.
func (d *Directory) Match(key string) (o Owner, ok, ready bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var hit *dirKey
	for i := range d.keys {
		k := &d.keys[i]
		if len(key) < len(k.prefix)+len(k.suffix) || !strings.HasPrefix(key, k.prefix) || !strings.HasSuffix(key, k.suffix) {
			continue
		}
		if hit != nil {
			return Owner{}, false, d.ready // two keys share the hint: neither is attributed
		}
		hit = k
	}
	if hit == nil || hit.creator == nil {
		return Owner{}, false, d.ready
	}
	return *hit.creator, true, d.ready
}

func (d *Directory) origin() string {
	if d.Origin != "" {
		return d.Origin
	}
	return "https://" + anthropicHost
}

func (d *Directory) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

// maxPages bounds a listing (1000 items a page).
const maxPages = 100

// pages GETs every page of an Admin API list into each, one item at a time.
func (d *Directory) pages(ctx context.Context, path string, each func(raw *pageItem)) error {
	after := ""
	for range maxPages {
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after_id", after)
		}
		var page struct {
			Data    []pageItem `json:"data"`
			HasMore bool       `json:"has_more"`
			LastID  string     `json:"last_id"`
		}
		if err := getJSON(ctx, d.client(), d.origin()+path+"?"+q.Encode(), map[string]string{
			"x-api-key": d.AdminKey, "anthropic-version": "2023-06-01",
		}, &page); err != nil {
			return err
		}
		for i := range page.Data {
			each(&page.Data[i])
		}
		if !page.HasMore || page.LastID == "" {
			return nil
		}
		after = page.LastID
	}
	return nil
}

// pageItem is an API key or a user, as the Admin API lists them.
type pageItem struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	PartialKeyHint string `json:"partial_key_hint"`
	CreatedBy      *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"created_by"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// list is the organization's active keys, each with its creator when that's
// a user.
func (d *Directory) list(ctx context.Context) ([]dirKey, error) {
	users := map[string]*Owner{}
	if err := d.pages(ctx, "/v1/organizations/users", func(u *pageItem) {
		if u.ID != "" {
			users[u.ID] = &Owner{AccountID: u.ID, Email: u.Email, Name: u.Name}
		}
	}); err != nil {
		return nil, err
	}
	var keys []dirKey
	err := d.pages(ctx, "/v1/organizations/api_keys", func(k *pageItem) {
		// The hint is the key's first and last characters around "...":
		// "sk-ant-api03-R2D...igAA".
		pre, suf, ok := strings.Cut(k.PartialKeyHint, "...")
		if !ok || pre == "" || suf == "" || k.Status != "" && k.Status != "active" {
			return
		}
		dk := dirKey{prefix: pre, suffix: suf}
		if cb := k.CreatedBy; cb != nil && (cb.Type == "" || cb.Type == "user") {
			dk.creator = users[cb.ID]
		}
		keys = append(keys, dk)
	})
	return keys, err
}
