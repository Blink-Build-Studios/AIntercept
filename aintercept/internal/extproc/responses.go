package extproc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
)

// All responses below are "CONTINUE, no mutation" — the observer only watches,
// it never modifies traffic. Each phase requires the matching response oneof so
// Envoy's state machine stays in sync.

func emptyCommon() *extprocv3.CommonResponse {
	return &extprocv3.CommonResponse{Status: extprocv3.CommonResponse_CONTINUE}
}

func continueRequestHeaders() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{Response: emptyCommon()},
		},
	}
}

func continueResponseHeaders() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &extprocv3.HeadersResponse{Response: emptyCommon()},
		},
	}
}

func continueRequestBody() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{Response: emptyCommon()},
		},
	}
}

func continueResponseBody() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{Response: emptyCommon()},
		},
	}
}

func continueRequestTrailers() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestTrailers{
			RequestTrailers: &extprocv3.TrailersResponse{},
		},
	}
}

func continueResponseTrailers() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseTrailers{
			ResponseTrailers: &extprocv3.TrailersResponse{},
		},
	}
}

// newCorrelationID is instance-scoped and stream-scoped. It needs to be unique
// per transaction across all replicas (no sticky routing), so it combines the
// instance ID, a per-instance stream counter, and random entropy.
func newCorrelationID(instance string, streamNum uint64) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", instance, streamNum, hex.EncodeToString(b[:]))
}
