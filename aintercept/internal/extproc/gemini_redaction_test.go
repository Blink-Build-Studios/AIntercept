package extproc

import (
	"encoding/json"
	"strings"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

const geminiKey = "AIzaSyTEST-not-a-real-key-0123456789"

// A Gemini key sent either way never reaches any emitted event, and both forms
// still attribute the call to the same caller.
func TestGeminiKeyNeverReachesEvents(t *testing.T) {
	run := func(reqHeaders *extprocv3.HttpHeaders) []events.Event {
		sink := &memSink{}
		proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
		srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
		st := &streamState{correlationID: "call-1"}
		srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: reqHeaders}})
		srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{
			RequestBody: &extprocv3.HttpBody{Body: []byte(`{"contents":[]}`), EndOfStream: true},
		}})
		srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
			ResponseHeaders: headers(":status", "200"),
		}})
		srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{
			ResponseBody: &extprocv3.HttpBody{Body: []byte(`{"usageMetadata":{"promptTokenCount":1}}`), EndOfStream: true},
		}})
		proc.Close()
		return sink.ev
	}

	viaQuery := run(headers(
		":authority", "generativelanguage.googleapis.com", ":method", "POST",
		":path", "/v1beta/models/gemini-2.0-flash:generateContent?key="+geminiKey,
	))
	viaHeader := run(headers(
		":authority", "generativelanguage.googleapis.com", ":method", "POST",
		":path", "/v1beta/models/gemini-2.0-flash:generateContent",
		"x-goog-api-key", geminiKey,
	))

	hashes := map[string]bool{}
	for name, evs := range map[string][]events.Event{"query": viaQuery, "header": viaHeader} {
		if len(evs) == 0 {
			t.Fatalf("%s: no events", name)
		}
		for _, e := range evs {
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), geminiKey) {
				t.Fatalf("%s: %s event carries the key: %s", name, e.Phase, raw)
			}
			if e.Phase == events.PhaseComplete {
				hashes[e.KeyHash] = true
			}
		}
	}
	if len(hashes) != 1 || hashes[""] {
		t.Fatalf("want one non-empty key_hash across both forms, got %v", hashes)
	}
}
