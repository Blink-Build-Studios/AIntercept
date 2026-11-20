package extproc

import (
	"net"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
)

// BenchmarkReplyPiece is the time one streamed text event spends in the
// enforcing filter, from arriving to being released: with an Interceptor
// (package intercept, a rule on reply text, over gRPC on loopback TCP)
// deciding every piece, and without one holding text.
func BenchmarkReplyPiece(b *testing.B) {
	for _, bc := range []struct {
		name  string
		items []intercept.Item
	}{
		{"decided", []intercept.Item{intercept.OnResponse(intercept.Contains("swordfish")).Replace("[password]")}},
		{"not held", []intercept.Item{intercept.OnRequest(intercept.Contains("swordfish")).Reply("no")}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			g := intercept.New(bc.items...).GRPC()
			go func() { _ = g.Serve(lis) }()
			defer g.Stop()
			ic, err := interceptor.New(interceptor.Config{Endpoint: lis.Addr().String(), Content: true, Deadline: 5 * time.Second}, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer ic.Close()
			proc := events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1 << 16})
			defer proc.Close()
			srv := New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
			st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
			srv.enforceHandle(st, eReqHeaders(false, anthropicCall...))
			srv.enforceHandle(st, eReqBody(`{"model":"m-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`, true))
			srv.enforceHandle(st, eRespHeaders(":status", "200", "content-type", "text/event-stream"))
			for _, ev := range replyFormats[0].events()[:4] {
				srv.enforceHandle(st, eRespBody([]byte(ev), false))
			}
			piece := []byte(sseEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"a word "}}`))
			b.ResetTimer()
			for range b.N {
				out := srv.enforceHandle(st, eRespBody(piece, false))
				if body, _ := streamed(out[0]); len(body) == 0 {
					b.Fatal("the piece wasn't released")
				}
			}
		})
	}
}
