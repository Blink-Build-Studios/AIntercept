package extproc

import (
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

func reqBody(b []byte, eos bool) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{Body: b, EndOfStream: eos}}}
}

func respBody(b []byte, eos bool) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: &extprocv3.HttpBody{Body: b, EndOfStream: eos}}}
}

// The budget counts what in-flight streams hold and returns to zero once the
// transaction is emitted and processed.
func TestBudgetTracksHTTPBuffersAndReleases(t *testing.T) {
	budget := membudget.New(1 << 30)
	proc := events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 64, Budget: budget})
	srv := New(Config{Processor: proc, Budget: budget, InstanceID: "test"})
	st := &streamState{correlationID: "b-1"}

	srv.handle(st, &extprocv3.ProcessingRequest{ObservabilityMode: true, Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "api.anthropic.com", ":method", "POST", ":path", "/v1/messages"),
	}})
	srv.observeBody(st, events.DirInbound, events.PhaseRequestBodyChunk, reqBody(make([]byte, 3000), true).GetRequestBody())
	if got := budget.Held(); got != 3000 {
		t.Fatalf("request buffered: held %d, want 3000", got)
	}
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{ResponseHeaders: headers(":status", "200")}})
	srv.observeBody(st, events.DirOutbound, events.PhaseResponseBodyChunk, respBody(make([]byte, 500), false).GetResponseBody())
	if got := budget.Held(); got != 3500 {
		t.Fatalf("response buffered: held %d, want 3500", got)
	}
	srv.observeBody(st, events.DirOutbound, events.PhaseResponseBodyChunk, respBody(make([]byte, 100), true).GetResponseBody())
	proc.Close() // drains: the complete event's bodies are released after extraction
	if budget.Held() != 0 || budget.Peak() < 3600 {
		t.Fatalf("after completion: held %d peak %d", budget.Held(), budget.Peak())
	}
}

// A WebSocket's partial message is counted while it waits, and released when
// the stream ends, however it ends.
func TestBudgetTracksWebSocketPartialMessage(t *testing.T) {
	budget := membudget.New(1 << 30)
	proc := events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 64, Budget: budget})
	srv := New(Config{Processor: proc, Budget: budget, InstanceID: "test"})
	st := &streamState{correlationID: "b-2"}
	srv.handle(st, &extprocv3.ProcessingRequest{ObservabilityMode: true, Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "chatgpt.com", ":method", "GET", ":path", "/ws", "upgrade", "websocket"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{ResponseHeaders: headers(":status", "101")}})
	frame := wsFrame(make([]byte, 5000), true)
	frame[0] = 0x81                              // uncompressed text
	srv.handle(st, reqBody(frame[:3000], false)) // most of one frame, then silence
	if got := budget.Held(); got != 3000 {
		t.Fatalf("partial frame: held %d, want 3000", got)
	}
	srv.wsClose(st)
	srv.budget.Add(-st.held) // what Process's deferred release does when the stream ends
	st.held = 0
	proc.Close()
	if budget.Held() != 0 {
		t.Fatalf("after close: held %d", budget.Held())
	}
}
