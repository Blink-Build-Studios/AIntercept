//go:build e2e

// No-drop tests (docs/STREAMING_PIPELINE.md, phase 1): through the stack in
// test/interceptor, every request that goes through is recorded in object
// storage exactly once, through an object-store outage, a hard kill of the
// observer, and a full spool. They run after the Interceptor tests (file order)
// and leave the stack as they found it.
package e2e

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

var (
	s3Endpoint = envOr("E2E_S3", "http://localhost:19000")
	statsURL   = envOr("E2E_OBSERVER_STATS", "http://localhost:19001/stats")
	// outage is how long the object store stays unreachable in the outage test.
	outage = envOr("E2E_OUTAGE", "2m")
)

const bucket, prefix = "e2e", "e2e/"

func dc(t *testing.T, args ...string) {
	t.Helper()
	if out, err := compose(args...).CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

// dcTry is dc for cleanup, where the state may already be what's wanted (e.g.
// unpausing a container that isn't paused).
func dcTry(args ...string) { _ = compose(args...).Run() }

func compose(args ...string) *exec.Cmd {
	a := append([]string{"compose", "--project-directory", composeDir}, strings.Fields(os.Getenv("E2E_COMPOSE_FILES"))...)
	return exec.Command("docker", append(a, args...)...)
}

func s3Client(t *testing.T) *s3.Client {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(s3Endpoint); o.UsePathStyle = true })
	if _, err := c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if !errors.As(err, &owned) && !errors.As(err, &exists) {
			t.Fatalf("create bucket: %v", err)
		}
	}
	return c
}

// stored reads every event the observer has uploaded.
func stored(t *testing.T, c *s3.Client) []events.Event {
	t.Helper()
	var out []events.Event
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(context.Background())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, o := range page.Contents {
			obj, err := c.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: o.Key})
			if err != nil {
				t.Fatalf("get %s: %v", *o.Key, err)
			}
			raw, _ := io.ReadAll(obj.Body)
			obj.Body.Close()
			gz, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("%s: not gzip: %v", *o.Key, err)
			}
			sc := bufio.NewScanner(gz)
			sc.Buffer(make([]byte, 1<<20), 64<<20)
			for sc.Scan() {
				var e events.Event
				if json.Unmarshal(sc.Bytes(), &e) == nil {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// recorded maps each request's x-e2e-id header to how many complete or
// disconnect rows (or ws_connection rows, for WebSockets) its call has.
func recorded(evs []events.Event, phase events.Phase) map[string]int {
	byCorr := map[string]string{}
	for _, e := range evs {
		if e.Phase == events.PhaseRequestHeaders {
			if id := e.Headers["x-e2e-id"]; id != "" {
				byCorr[e.CorrelationID] = id
			}
		}
	}
	out := map[string]int{}
	for _, e := range evs {
		if id := byCorr[e.CorrelationID]; id != "" && (e.Phase == phase || (phase == events.PhaseComplete && e.Phase == events.PhaseDisconnect)) {
			out[id]++
		}
	}
	return out
}

type observerStats struct {
	LastResortDrops uint64 `json:"LastResortDrops"`
	Writer          struct {
		SpoolFiles int  `json:"spool_files"`
		Full       bool `json:"full"`
	} `json:"writer"`
}

func stats(t *testing.T) (observerStats, bool) {
	t.Helper()
	resp, err := http.Get(statsURL)
	if err != nil {
		return observerStats{}, false
	}
	defer resp.Body.Close()
	var s observerStats
	return s, json.NewDecoder(resp.Body).Decode(&s) == nil
}

func waitUntil(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(time.Second)
	}
}

// traffic sends n requests of mixed shapes (plain HTTP, SSE streaming, an
// analytics batch) through the observing filter, each tagged with an id.
func traffic(t *testing.T, run string, n int, host string) []string {
	t.Helper()
	paths := []struct{ path, body string }{
		{"/v1/messages", `{"model":"fixture-model","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", prompt}, // stream: true
		{"/api/event_logging/batch", `{"events":[{"event_type":"ClaudeCodeInternalEvent","event_data":{"event_name":"tengu_e2e","session_id":"s"}}]}`},
	}
	var ids []string
	for i := 0; i < n; i++ {
		p := paths[i%len(paths)]
		id := fmt.Sprintf("%s-%03d", run, i)
		req, _ := http.NewRequest("POST", "http://"+envoyAddr+p.path, strings.NewReader(p.body))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "claude-cli/9.9.9 (external, cli)")
		req.Header.Set("x-e2e-id", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %s: %v", id, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("request %s: status %d", id, resp.StatusCode)
		}
		ids = append(ids, id)
	}
	return ids
}

func assertEachRecordedOnce(t *testing.T, c *s3.Client, ids []string, phase events.Phase) {
	t.Helper()
	var got map[string]int
	waitUntil(t, 3*time.Minute, "every request in object storage", func() bool {
		got = recorded(stored(t, c), phase)
		for _, id := range ids {
			if got[id] == 0 {
				return false
			}
		}
		return true
	})
	for _, id := range ids {
		if got[id] != 1 {
			t.Errorf("%s recorded %d times, want exactly 1", id, got[id])
		}
	}
}

func spoolDrained(t *testing.T) func() bool {
	return func() bool { s, ok := stats(t); return ok && s.Writer.SpoolFiles == 0 && !s.Writer.Full }
}

// An object-store outage loses nothing: files wait in the spool and upload
// when the store is back.
func TestZZNoLossThroughStoreOutage(t *testing.T) {
	c := s3Client(t)
	ids := traffic(t, "outage-before", 15, "observe.test")
	dc(t, "pause", "moto")
	defer dcTry("unpause", "moto")
	ids = append(ids, traffic(t, "outage-during", 15, "observe.test")...)
	d, _ := time.ParseDuration(outage)
	time.Sleep(d)
	dc(t, "unpause", "moto")
	waitUntil(t, 3*time.Minute, "the spool to drain", spoolDrained(t))
	assertEachRecordedOnce(t, c, ids, events.PhaseComplete)
}

// A hard kill of the observer loses nothing that was sealed to the spool: the
// restarted observer uploads it.
func TestZZNoLossThroughObserverKill(t *testing.T) {
	c := s3Client(t)
	dc(t, "pause", "moto")
	ids := traffic(t, "kill", 15, "observe.test")
	time.Sleep(15 * time.Second) // past the 10 s flush: sealed into the spool, not uploaded
	if s, ok := stats(t); !ok || s.Writer.SpoolFiles == 0 {
		t.Fatalf("expected spooled files before the kill: %+v", s)
	}
	dc(t, "kill", "-s", "KILL", "observer")
	dc(t, "unpause", "moto")
	dc(t, "start", "observer")
	waitUntil(t, 2*time.Minute, "the observer to come back", func() bool { _, ok := stats(t); return ok })
	waitUntil(t, 3*time.Minute, "the spool to drain", spoolDrained(t))
	assertEachRecordedOnce(t, c, ids, events.PhaseComplete)
}

// With the spool full: observed traffic keeps flowing (drops are counted, as a
// last resort), enforced calls are refused; once it drains, both work again.
func TestZZSpoolFull(t *testing.T) {
	t.Setenv("OBSERVER_SPOOL_MAX_BYTES", "20000")
	dc(t, "up", "-d", "observer")
	defer func() {
		os.Unsetenv("OBSERVER_SPOOL_MAX_BYTES")
		dcTry("unpause", "moto")
		dc(t, "up", "-d", "observer")
		waitUntil(t, 2*time.Minute, "the observer to come back", func() bool { _, ok := stats(t); return ok })
	}()
	waitUntil(t, time.Minute, "the observer with a small spool", func() bool { _, ok := stats(t); return ok })
	dc(t, "pause", "moto")
	waitUntil(t, 2*time.Minute, "the spool to fill", func() bool {
		_ = traffic(t, "fill", 10, "observe.test") // observe mode: always 200
		s, _ := stats(t)
		return s.Writer.Full
	})
	_ = traffic(t, "full", 10, "observe.test") // still 200 with the spool full
	if s, _ := stats(t); s.LastResortDrops == 0 {
		t.Errorf("no last-resort drops counted with the spool full")
	}
	if resp, body := post(t, "enforce.test", "/v1/messages", prompt); resp.StatusCode == 200 {
		t.Errorf("enforced call went through with the spool full: %d %s", resp.StatusCode, body)
	}
	if resp, err := http.Get(strings.Replace(statsURL, "/stats", "/healthz/ready", 1)); err != nil || resp.StatusCode != 200 {
		t.Errorf("observer not ready with the spool full (%v %v); readiness must not depend on the spool", err, resp)
	} else {
		resp.Body.Close()
	}
	dc(t, "unpause", "moto")
	waitUntil(t, 3*time.Minute, "the spool to drain", spoolDrained(t))
	if resp, body := post(t, "enforce.test", "/v1/messages", prompt); resp.StatusCode != 200 {
		t.Errorf("enforced call refused after the spool drained: %d %s", resp.StatusCode, body)
	}
}

// Shutting down in order (Envoy first, then the observer) records every open
// WebSocket's connection row.
func TestZZZGracefulShutdownRecordsWebSockets(t *testing.T) {
	c := s3Client(t)
	var ids []string
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("ws-%d", i)
		conn, r := wsDialTagged(t, "observe.test", id)
		_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
		_, _ = conn.Write(wsframe.Encode(wsframe.OpText, []byte("hello"), true))
		_, _ = wsRead(t, r)
		ids = append(ids, id)
		defer conn.Close()
	}
	dc(t, "stop", "envoy")
	dc(t, "stop", "observer")
	dc(t, "start", "observer")
	dc(t, "start", "envoy")
	waitUntil(t, 2*time.Minute, "the observer to come back", func() bool { _, ok := stats(t); return ok })
	assertEachRecordedOnce(t, c, ids, events.PhaseWSConnection)
}
