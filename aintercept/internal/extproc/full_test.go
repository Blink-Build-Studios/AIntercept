package extproc

import (
	"context"
	"io"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

// fakeStream replays requests and records responses.
type fakeStream struct {
	grpc.ServerStream
	reqs []*extprocv3.ProcessingRequest
	sent []*extprocv3.ProcessingResponse
}

func (f *fakeStream) Context() context.Context     { return context.Background() }
func (f *fakeStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeStream) SetTrailer(metadata.MD)       {}
func (f *fakeStream) Send(r *extprocv3.ProcessingResponse) error {
	f.sent = append(f.sent, r)
	return nil
}
func (f *fakeStream) Recv() (*extprocv3.ProcessingRequest, error) {
	if len(f.reqs) == 0 {
		return nil, io.EOF
	}
	r := f.reqs[0]
	f.reqs = f.reqs[1:]
	return r, nil
}

func enforceFirst() *extprocv3.ProcessingRequest {
	r := eReqHeaders(true, anthropicCall...)
	r.ProtocolConfig = &extprocv3.ProtocolConfiguration{RequestBodyMode: extprocv3ProcessingModeFullDuplex}
	return r
}

// With the spool full, an enforced call is refused at its first message, so
// Envoy's enforcing filter (failure_mode_allow: false) rejects it rather than
// letting it through unrecorded. Observed (async) streams carry on.
func TestEnforceRefusesNewStreamsWhenSpoolFull(t *testing.T) {
	full := true
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 64})
	ic := interceptortest.NewClient(t, redactor(), interceptor.Config{Deadline: 5e9}, nil)
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic, Full: func() bool { return full }})

	err := srv.Process(&fakeStream{reqs: []*extprocv3.ProcessingRequest{enforceFirst()}})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("enforced stream with the spool full: err %v, want Unavailable", err)
	}

	observed := eReqHeaders(true, anthropicCall...)
	observed.ObservabilityMode = true
	if err := srv.Process(&fakeStream{reqs: []*extprocv3.ProcessingRequest{observed}}); err != nil {
		t.Fatalf("observed stream with the spool full: %v, want it served", err)
	}

	full = false
	if err := srv.Process(&fakeStream{reqs: []*extprocv3.ProcessingRequest{enforceFirst()}}); status.Code(err) == codes.Unavailable {
		t.Fatalf("enforced stream refused after the spool drained")
	}
	proc.Close()
}
