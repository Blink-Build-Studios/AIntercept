package extproc

import (
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The complete row carries the call's timing and sizes, so one row describes it.
func TestCompleteRowCarriesTimingAndBytes(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
	srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
	st := &streamState{correlationID: "call-1"}

	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "api.anthropic.com", ":method", "POST", ":path", "/v1/messages"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{
		RequestBody: &extprocv3.HttpBody{Body: []byte(`{"model":"claude-x"}`), EndOfStream: true},
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: headers(":status", "200"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{
		ResponseBody: &extprocv3.HttpBody{Body: []byte(`{"id":"msg_1"}`), EndOfStream: true},
	}})
	proc.Close()

	at := map[events.Phase]events.Event{}
	for _, e := range sink.ev {
		at[e.Phase] = e
	}
	c, ok := at[events.PhaseComplete]
	if !ok {
		t.Fatalf("no complete row in %d events", len(sink.ev))
	}
	if c.RequestStartNS != at[events.PhaseRequestHeaders].TimestampNS || c.ResponseStartNS != at[events.PhaseResponseHeaders].TimestampNS {
		t.Fatalf("starts %d/%d, want the header rows' timestamps", c.RequestStartNS, c.ResponseStartNS)
	}
	if !(c.RequestStartNS > 0 && c.RequestStartNS <= c.ResponseStartNS && c.ResponseStartNS <= c.TimestampNS) {
		t.Fatalf("times out of order: %d %d %d", c.RequestStartNS, c.ResponseStartNS, c.TimestampNS)
	}
	if c.RequestBytes != 20 || c.ResponseBytes != 14 {
		t.Fatalf("bytes %d/%d, want 20/14", c.RequestBytes, c.ResponseBytes)
	}
}
