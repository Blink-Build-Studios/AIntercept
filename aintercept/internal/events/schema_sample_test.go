package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"
)

// recordingSink captures every event it receives, for assertions.
type recordingSink struct {
	mu  sync.Mutex
	got []Event
}

func (r *recordingSink) Emit(e Event) error {
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	return nil
}
func (r *recordingSink) Flush() error { return nil }
func (r *recordingSink) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.got))
	copy(out, r.got)
	return out
}

// End-to-end through the processor: an unrecognized transaction lands on the
// schema sink (as a fingerprint) and NOT the schema sink when fully recognized.
func TestProcessor_RoutesUnrecognizedToSchemaSink(t *testing.T) {
	main := &recordingSink{}
	schema := &recordingSink{}
	p := NewProcessor(main, ProcessorConfig{BufferSize: 64, SchemaSink: schema})

	// Unrecognized: a POST body we can't parse a model/usage out of.
	unknown := Event{Phase: PhaseComplete, Host: "h", Method: "POST", Path: "/v9/unknown"}
	unknown.AttachBodies([]byte(`{"weird":"shape"}`), []byte(`{"also":"weird"}`), "", nil)
	p.Offer(unknown)

	// Recognized: model + usage both present → no sample.
	known := Event{Phase: PhaseComplete, Host: "h", Method: "POST", Path: "/v1/messages"}
	known.AttachBodies([]byte(`{"model":"claude-opus-4-8","messages":[],"tools":[{"name":"Bash"}]}`),
		[]byte(`{"usage":{"input_tokens":5,"output_tokens":2}}`), "", nil)
	p.Offer(known)

	p.Close()

	if len(main.events()) != 2 {
		t.Fatalf("main sink should get both events, got %d", len(main.events()))
	}
	se := schema.events()
	if len(se) != 1 {
		t.Fatalf("schema sink should get exactly 1 sample, got %d", len(se))
	}
	if se[0].SchemaFingerprint == nil || se[0].Path != "/v9/unknown" {
		t.Fatalf("wrong schema sample: %+v", se[0])
	}
	// Main-sink events must NOT carry a fingerprint (that's a schema-sink-only
	// field) and must not carry content either.
	for _, e := range main.events() {
		if e.SchemaFingerprint != nil {
			t.Fatal("normal telemetry event wrongly carries a schema fingerprint")
		}
	}
}

// A fully-recognized transaction (model + usage both extracted) is NOT sampled.
func TestSample_FullyRecognizedNotSampled(t *testing.T) {
	e := Event{Phase: PhaseComplete, Path: "/v1/messages"}
	e.AttachBodies(
		[]byte(`{"model":"claude-opus-4-8","messages":[],"tools":[{"name":"Bash"}]}`),
		[]byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`),
		"", nil,
	)
	e.ExtractDeferred()
	if _, ok := e.SampleIfUnrecognized(); ok {
		t.Fatal("fully-recognized transaction must not be sampled")
	}
}

// A transaction with no model and no usage IS sampled, with a structural
// skeleton and the correct reason.
func TestSample_UnknownShapeSampled(t *testing.T) {
	reqBody := []byte(`{"brand_new_field":"super secret prompt text","nested":{"count":3,"flag":true},"items":["a","b","c"]}`)
	e := Event{
		Phase:  PhaseComplete,
		Host:   "api.newprovider.example",
		Method: "POST",
		Path:   "/v2/infer",
		Status: 200,
	}
	e.AttachBodies(reqBody, []byte(`{"unexpected":"response body with secrets"}`), "",
		map[string]string{"content-type": "application/json", "x-provider-trace": "abc123"})
	e.AttachRequestHeaders(map[string]string{
		"content-type":  "application/json",
		"authorization": "<redacted>",
		"user-agent":    "newprovider-sdk/1.2.3",
	})
	e.ExtractDeferred()

	sample, ok := e.SampleIfUnrecognized()
	if !ok {
		t.Fatal("unknown shape must be sampled")
	}
	fp := sample.SchemaFingerprint
	if fp == nil {
		t.Fatal("sample missing fingerprint")
	}
	if fp.Reason != "no_model,no_usage" {
		t.Fatalf("reason = %q, want no_model,no_usage", fp.Reason)
	}
	// Routing metadata is carried for attribution.
	if sample.Host != "api.newprovider.example" || sample.Path != "/v2/infer" || sample.Status != 200 {
		t.Fatalf("routing metadata not carried: %+v", sample)
	}
	if fp.Request == nil || fp.Request.Kind != "json" {
		t.Fatalf("request shape not json: %+v", fp.Request)
	}
	// Safe header value (user-agent) retained; the skeleton shows structure.
	if fp.SafeHeaders["user-agent"] != "newprovider-sdk/1.2.3" {
		t.Fatalf("safe header not retained: %+v", fp.SafeHeaders)
	}
	// Header NAMES present include the sensitive one (presence is structure), but
	// its VALUE must never appear anywhere in the fingerprint.
	if !containsStr(fp.RequestHeaderNames, "authorization") {
		t.Fatalf("request header names should list authorization: %+v", fp.RequestHeaderNames)
	}
}

// THE critical invariant: a schema sample serialized to a sink contains NO
// scalar content — no prompt text, no response text, no key values.
func TestSample_NeverContainsContent(t *testing.T) {
	const prompt = "SUPERSECRETPROMPT_do_not_leak"
	const respText = "SUPERSECRETRESPONSE_do_not_leak"
	const apiKey = "sk-ant-REALKEYMATERIAL-shouldneverappear"
	reqBody := []byte(`{"messages":[{"role":"user","content":"` + prompt + `"}],"x":"` + prompt + `"}`)
	respBody := []byte(`{"answer":"` + respText + `"}`)

	e := Event{Phase: PhaseComplete, Host: "h", Method: "POST", Path: "/unknown"}
	e.AttachBodies(reqBody, respBody, "", map[string]string{"content-type": "application/json"})
	e.AttachRequestHeaders(map[string]string{
		"content-type":  "application/json",
		"authorization": "Bearer " + apiKey, // raw-looking value; must not survive
	})
	e.ExtractDeferred()

	sample, ok := e.SampleIfUnrecognized()
	if !ok {
		t.Fatal("expected a sample")
	}
	b, err := json.Marshal(sample.WithoutContent())
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{prompt, respText, apiKey, "REALKEYMATERIAL"} {
		if bytes.Contains(b, []byte(needle)) {
			t.Fatalf("schema sample leaked %q:\n%s", needle, b)
		}
	}
	// It should, however, carry the key paths (structure) so it's useful.
	if !bytes.Contains(b, []byte("messages")) || !bytes.Contains(b, []byte("role")) {
		t.Fatalf("sample should retain key PATHS for structure: %s", b)
	}
}

// Contract: object KEYS are kept verbatim (they are the schema we want to read);
// scalar VALUES are always scrubbed to a type. This is deliberate — we do NOT
// heuristically redact "sensitive-looking" keys. Operators must not put secrets
// in key position. This test locks the behavior so it isn't "fixed" as a leak.
func TestSample_KeysKeptValuesScrubbed(t *testing.T) {
	const keyName = "usage_by_region_field"   // a key: MUST be preserved
	const valueSecret = "SECRET_VALUE_leakme" // a value: MUST be scrubbed
	body := []byte(`{"` + keyName + `":"` + valueSecret + `","n":12345}`)
	e := Event{Phase: PhaseComplete, Path: "/unknown"}
	e.AttachBodies(body, nil, "", nil)
	e.ExtractDeferred()
	sample, ok := e.SampleIfUnrecognized()
	if !ok {
		t.Fatal("expected sample")
	}
	b, _ := json.Marshal(sample.WithoutContent())
	if !bytes.Contains(b, []byte(keyName)) {
		t.Fatalf("key should be preserved verbatim: %s", b)
	}
	if bytes.Contains(b, []byte(valueSecret)) {
		t.Fatalf("scalar VALUE leaked (must be scrubbed to a type): %s", b)
	}
	// The value is represented by its type, not its content.
	if !bytes.Contains(b, []byte("str(")) || !bytes.Contains(b, []byte(`"int"`)) {
		t.Fatalf("values should be reduced to types: %s", b)
	}
}

// The fingerprinter must not panic on malformed/huge/binary input — it is
// fail-open and returns a best-effort shape or nil.
func TestSample_FailOpenOnGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte("not json at all"),
		[]byte("\x00\x01\x02\xff\xfe"),          // binary
		[]byte(strings.Repeat(`{"a":`, 100000)), // deeply unbalanced
		[]byte(`{"` + strings.Repeat("k", 1<<20) + `":1}`), // one giant key
	}
	for i, body := range cases {
		e := Event{Phase: PhaseComplete, Path: "/x"}
		e.AttachBodies(body, nil, "", nil)
		e.ExtractDeferred()
		// Must not panic.
		_, _ = e.SampleIfUnrecognized()
		_ = i
	}
}

// A compressed body that doesn't decode (here: corrupt gzip) is classified as
// binary.
func TestSample_UndecodableBodyIsBinary(t *testing.T) {
	e := Event{Phase: PhaseComplete, Path: "/x"}
	e.AttachBodies([]byte("not-recognized"), []byte("\x1f\x8bcompressed-bytes"), "gzip", nil)
	e.ExtractDeferred()
	sample, ok := e.SampleIfUnrecognized()
	if !ok {
		t.Fatal("expected sample")
	}
	if sample.SchemaFingerprint.Response == nil || sample.SchemaFingerprint.Response.Kind != "binary" {
		t.Fatalf("compressed response should be binary, got %+v", sample.SchemaFingerprint.Response)
	}
}

// sampleLimiter caps emissions per window.
func TestSampleLimiter_CapsPerWindow(t *testing.T) {
	l := newSampleLimiter(3)
	allowed := 0
	for i := 0; i < 10; i++ {
		if l.allow() {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("limiter allowed %d, want 3", allowed)
	}
}

// panicOnceSink panics on the first Emit, then behaves. Proves the drain loop's
// recover keeps telemetry flowing when one event trips a panic.
type panicOnceSink struct {
	mu       sync.Mutex
	panicked bool
	got      []Event
}

func (s *panicOnceSink) Emit(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.panicked {
		s.panicked = true
		panic("boom in sink")
	}
	s.got = append(s.got, e)
	return nil
}
func (s *panicOnceSink) Flush() error { return nil }

func TestProcessor_PanicInEmitDoesNotKillDrain(t *testing.T) {
	sink := &panicOnceSink{}
	p := NewProcessor(sink, ProcessorConfig{BufferSize: 16})
	p.Offer(Event{Seq: 1, Path: "/a"}) // triggers panic, recovered
	p.Offer(Event{Seq: 2, Path: "/b"}) // must still be processed
	p.Offer(Event{Seq: 3, Path: "/c"})
	p.Close()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.got) != 2 {
		t.Fatalf("drain goroutine died after panic: later events=%d, want 2", len(sink.got))
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func brotliBytes(t *testing.T, plain string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A Brotli response is decoded before fingerprinting: the skeleton shows its
// structure (types only), while ByteLen/ContentEncoding describe the wire.
func TestSample_BrotliBodyIsFingerprinted(t *testing.T) {
	wire := brotliBytes(t, `{"account":{"email":"someone@example.com","id":"secret-id"}}`)
	e := Event{Phase: PhaseComplete, Path: "/api/oauth/profile"}
	e.AttachBodies(nil, wire, "br", nil)
	e.ExtractDeferred()
	sample, ok := e.SampleIfUnrecognized()
	if !ok {
		t.Fatal("expected sample")
	}
	r := sample.SchemaFingerprint.Response
	if r == nil || r.Kind != "json" || r.ContentEncoding != "br" || r.ByteLen != len(wire) {
		t.Fatalf("want a json shape of the br wire body, got %+v", r)
	}
	if sk := string(r.Skeleton); strings.Contains(sk, "someone@example.com") || strings.Contains(sk, "secret-id") || !strings.Contains(sk, "email") {
		t.Fatalf("skeleton must carry keys and types, never values: %s", sk)
	}
}

// Synthetic (health-probe) traffic never spends the sampling budget.
func TestProcessor_SkipsSyntheticSamples(t *testing.T) {
	main := &recordingSink{}
	schema := &recordingSink{}
	p := NewProcessor(main, ProcessorConfig{BufferSize: 16, SchemaSink: schema})
	probe := Event{Phase: PhaseComplete, Host: "h", Method: "GET", Path: "/", Synthetic: true}
	probe.AttachBodies(nil, []byte(`{"not":"recognized"}`), "", nil)
	p.Offer(probe)
	p.Close()
	if len(main.events()) != 1 {
		t.Fatalf("synthetic events are still recorded, got %d", len(main.events()))
	}
	if n := len(schema.events()); n != 0 {
		t.Fatalf("synthetic events must not be sampled, got %d samples", n)
	}
}

// A model call with model+usage but no conversation/tools in a shape we read
// (a new request format) is still sampled, so lost tool visibility surfaces.
func TestSample_NoConversationIsSampled(t *testing.T) {
	resp := []byte(`{"usage":{"input_tokens":5,"output_tokens":2}}`)
	threaded := Event{Phase: PhaseComplete, Path: "/v1/messages"}
	threaded.AttachBodies([]byte(`{"model":"claude-opus-5-5","thread":{"id":"t1"},"turn":[{"type":"text"}]}`), resp, "", nil)
	threaded.ExtractDeferred()
	sample, ok := threaded.SampleIfUnrecognized()
	if !ok || sample.SchemaFingerprint.Reason != "no_conversation" {
		t.Fatalf("want a no_conversation sample, got ok=%v %+v", ok, sample.SchemaFingerprint)
	}
	normal := Event{Phase: PhaseComplete, Path: "/v1/messages"}
	normal.AttachBodies([]byte(`{"model":"claude-opus-5-5","messages":[],"tools":[{"name":"Bash"}]}`), resp, "", nil)
	normal.ExtractDeferred()
	if _, ok := normal.SampleIfUnrecognized(); ok {
		t.Fatal("a normal messages request must not be sampled")
	}
}

// Messages but no top-level tool catalog: sampled as no_tools, so tools
// declared somewhere new get noticed.
func TestSample_NoToolsIsSampled(t *testing.T) {
	e := Event{Phase: PhaseComplete, Path: "/v1/messages"}
	e.AttachBodies([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"usage":{"input_tokens":5,"output_tokens":2}}`), "", nil)
	e.ExtractDeferred()
	sample, ok := e.SampleIfUnrecognized()
	if !ok || sample.SchemaFingerprint.Reason != "no_tools" {
		t.Fatalf("want a no_tools sample, got ok=%v %+v", ok, sample.SchemaFingerprint)
	}
}
