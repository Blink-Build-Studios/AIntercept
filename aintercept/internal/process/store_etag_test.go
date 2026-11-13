package process

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestNormalizeETag(t *testing.T) {
	for in, want := range map[string]string{`"abc"`: "abc", "abc": "abc", "": "", `""`: ""} {
		if got := NormalizeETag(in); got != want {
			t.Errorf("NormalizeETag(%q) = %q, want %q", in, got, want)
		}
	}
}

// What S3Store sends and how it reads the answers: DO Spaces 412s a quoted
// If-Match even when it's current, so the ETag always goes out unquoted.
func TestS3ConditionalRequests(t *testing.T) {
	type req struct{ method, ifMatch, ifNoneMatch string }
	var (
		mu   sync.Mutex
		reqs []req
		// The next response: status, S3 error code (empty: success).
		status = http.StatusOK
		code   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, req{r.Method, r.Header.Get("If-Match"), r.Header.Get("If-None-Match")})
		if code != "" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(status)
			if r.Method != http.MethodHead {
				io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>m</Message></Error>`)
			}
			return
		}
		w.Header().Set("ETag", `"0f0f"`)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx := context.Background()
	s, err := NewS3Store(ctx, S3Config{Bucket: "b", Region: "us-east-1", Endpoint: srv.URL,
		AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	respond := func(st int, c string) {
		mu.Lock()
		status, code, reqs = st, c, nil
		mu.Unlock()
	}
	last := func() req {
		mu.Lock()
		defer mu.Unlock()
		if len(reqs) != 1 {
			t.Fatalf("%d requests, want 1: %+v", len(reqs), reqs)
		}
		return reqs[0]
	}

	respond(http.StatusOK, "")
	if e, err := s.ETag(ctx, "k"); err != nil || e != "0f0f" {
		t.Fatalf("ETag = %q %v, want 0f0f unquoted", e, err)
	}
	if r := last(); r.method != http.MethodHead {
		t.Fatalf("ETag sent %s, want HEAD", r.method)
	}
	respond(http.StatusNotFound, "NotFound")
	if _, err := s.ETag(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ETag of a missing key: %v, want ErrNotFound", err)
	}

	for _, given := range []string{"0f0f", `"0f0f"`} {
		respond(http.StatusOK, "")
		e, err := s.PutIf(ctx, "k", []byte("v"), given)
		if err != nil || e != "0f0f" {
			t.Fatalf("PutIf = %q %v", e, err)
		}
		if r := last(); r.method != http.MethodPut || r.ifMatch != "0f0f" || r.ifNoneMatch != "" {
			t.Fatalf("PutIf(%s) sent %+v, want If-Match: 0f0f unquoted", given, r)
		}
	}
	respond(http.StatusOK, "")
	if _, err := s.PutIf(ctx, "k", []byte("v"), ""); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.ifMatch != "" || r.ifNoneMatch != "*" {
		t.Fatalf("a create sent %+v, want If-None-Match: *", r)
	}

	// Lost races: 412 (DO Spaces, also for If-Match on a missing object), AWS's
	// 404 for If-Match on a missing object, and 409 for a concurrent conditional write.
	for _, c := range []struct {
		status int
		code   string
		etag   string
	}{
		{http.StatusPreconditionFailed, "PreconditionFailed", "0f0f"},
		{http.StatusPreconditionFailed, "PreconditionFailed", ""},
		{http.StatusNotFound, "NoSuchKey", "0f0f"},
		{http.StatusConflict, "ConditionalRequestConflict", "0f0f"},
	} {
		respond(c.status, c.code)
		if _, err := s.PutIf(ctx, "k", []byte("v"), c.etag); !errors.Is(err, ErrPrecondition) {
			t.Fatalf("%d %s (etag %q): %v, want ErrPrecondition", c.status, c.code, c.etag, err)
		}
	}
	// Anything else is an error, not a lost race.
	respond(http.StatusForbidden, "AccessDenied")
	if _, err := s.PutIf(ctx, "k", []byte("v"), "0f0f"); err == nil || errors.Is(err, ErrPrecondition) {
		t.Fatalf("403: %v, want a plain error", err)
	}
}
