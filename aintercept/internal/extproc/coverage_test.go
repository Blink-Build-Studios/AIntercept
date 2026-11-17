package extproc

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

// Coverage: every reply in both modes reaches the Interceptor, whatever its
// headers say (docs/INTERCEPTOR_V2.md, "What the Interceptor sees").

// codexReplyHeaders are a Codex HTTP reply's headers as chatgpt.com sends
// them (values synthetic): no content-type and no content-encoding, though
// the body is an event stream.
var codexReplyHeaders = []string{
	":status", "200",
	"cf-cache-status", "DYNAMIC",
	"cf-ray", "0000000000000000-AAA",
	"cross-origin-opener-policy", "same-origin-allow-popups",
	"date", "Mon, 16 Nov 2026 00:00:00 GMT",
	"referrer-policy", "strict-origin-when-cross-origin",
	"server", "cloudflare",
	"strict-transport-security", "max-age=31536000; includeSubDomains; preload",
	"x-codex-active-limit", "premium",
	"x-codex-plan-type", "pro",
	"x-codex-primary-used-percent", "1",
	"x-codex-primary-window-minutes", "300",
	"x-codex-turn-state", "synthetic-turn-state",
	"x-content-type-options", "nosniff",
	"x-oai-request-id", "00000000-0000-4000-8000-000000000000",
}

// codexEvents is a Codex reply's events with its first event split as
// chatgpt.com sends it: the event line alone in the first chunk.
func codexEvents(pieces ...string) []string {
	evs := replyFormats[1].events(pieces...)
	name, rest, _ := strings.Cut(evs[0], "\n")
	return append([]string{name + "\n", rest}, evs[1:]...)
}

func codexRig(t *testing.T, f *interceptortest.FakeV2) *v2Rig {
	t.Helper()
	rf := replyFormats[1]
	r := newBingRig(t, f, interceptor.Config{})
	r.send(eReqHeaders(false, rf.call...))
	r.send(eReqBody(rf.body, true))
	if out := r.send(eRespHeaders(codexReplyHeaders...)); len(out) != 0 {
		t.Fatalf("the reply's headers must wait for its first bytes: %v", out)
	}
	return r
}

// Enforce: a Codex reply without a content-type is an event stream, read
// event by event: its text is decided by OnReply.
func TestV2CodexReplyWithoutContentType(t *testing.T) {
	r := codexRig(t, &interceptortest.FakeV2{Reply: watchReply})
	out := r.stream(codexEvents("The pur", "ple ele", "phant is here."))
	if got := responsesText(t, sseDataLines(out)); got != "The [redacted] is here." || strings.Contains(out, "elephant") {
		t.Fatalf("text %q\n%s", got, out)
	}
}

// Accepted, it goes on byte for byte; its tool calls go to OnToolCall.
func TestV2CodexReplyWithoutContentTypeAccepted(t *testing.T) {
	f := &interceptortest.FakeV2{Reply: func(*pb2.ResponseMessage) *pb2.MessageVerdict { return &pb2.MessageVerdict{} }}
	r := codexRig(t, f)
	evs := codexEvents("Hello, ", "world.")
	if out := r.stream(evs); out != strings.Join(evs, "") {
		t.Fatalf("accepted reply changed:\n%s", out)
	}
	if len(f.RepliesSeen()) == 0 {
		t.Fatal("OnReply never called")
	}
}

// Without a content-type, a JSON body is decided whole, and anything else
// goes on as it came.
func TestV2ReplyWithoutContentTypeSniffed(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		r := codexRig(t, &interceptortest.FakeV2{Reply: watchReply})
		jsonOut := replyFormats[1].jsonOut
		if out := r.stream([]string{" \n", jsonOut}); out != " \n"+strings.Replace(jsonOut, phrase, redacted, 1) {
			t.Fatalf("got\n%s", out)
		}
	})
	t.Run("other", func(t *testing.T) {
		f := &interceptortest.FakeV2{Reply: watchReply}
		r := codexRig(t, f)
		if out := r.stream([]string{"purple elephant", " says hi"}); out != "purple elephant says hi" {
			t.Fatalf("got %q", out)
		}
		if len(f.RepliesSeen()) != 0 {
			t.Fatalf("OnReply got %v", f.RepliesSeen())
		}
	})
	t.Run("empty", func(t *testing.T) {
		r := codexRig(t, &interceptortest.FakeV2{Reply: watchReply})
		if out := r.stream([]string{""}); out != "" {
			t.Fatalf("got %q", out)
		}
	})
}

// observeRig is an observe-mode rig whose Interceptor accepts every reply.
func observeRig(t *testing.T) *v2Rig {
	t.Helper()
	r := newV2Rig(t, false)
	r.fake.Reply = func(*pb2.ResponseMessage) *pb2.MessageVerdict { return &pb2.MessageVerdict{} }
	return r
}

func (r *v2Rig) observeCall(rf replyFormat, respHeaders []string, chunks [][]byte) string {
	r.srv.handle(r.st, eReqHeaders(false, rf.call...))
	r.srv.handle(r.st, eReqBody(rf.body, true))
	r.srv.handle(r.st, eRespHeaders(respHeaders...))
	for i, c := range chunks {
		r.srv.handle(r.st, eRespBody(c, i == len(chunks)-1))
	}
	r.ic.Close()
	var text string
	for _, m := range r.fake.RepliesSeen() {
		text += m.GetMessage().GetParts()[0].GetText().GetText()
	}
	return text
}

func chunksOf(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

// Observe: the same Codex reply reaches OnReply.
func TestV2ObserveCodexReplyWithoutContentType(t *testing.T) {
	r := observeRig(t)
	if text := r.observeCall(replyFormats[1], codexReplyHeaders, chunksOf(codexEvents("The pur", "ple ele", "phant is here."))); text != "The purple elephant is here." {
		t.Fatalf("OnReply saw %q", text)
	}
}

func encode(t *testing.T, enc string, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch enc {
	case "gzip":
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
	case "br":
		w := brotli.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
	case "zstd":
		w, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
		_ = w.Close()
	}
	return buf.Bytes()
}

// Observe: an encoded reply (observe can't ask for it plain) is decoded, as
// the extractors do, and its text reaches OnReply: an event stream or a
// whole JSON body, in each encoding.
func TestV2ObserveEncodedReply(t *testing.T) {
	for _, enc := range []string{"gzip", "br", "zstd"} {
		for _, rf := range replyFormats {
			t.Run(enc+"/"+rf.name+"/sse", func(t *testing.T) {
				body := encode(t, enc, []byte(strings.Join(rf.events("The pur", "ple ele", "phant is here."), "")))
				half := len(body) / 2
				r := observeRig(t)
				text := r.observeCall(rf, []string{":status", "200", "content-type", "text/event-stream", "content-encoding", enc}, [][]byte{body[:half], body[half:]})
				if text != "The purple elephant is here." {
					t.Fatalf("OnReply saw %q", text)
				}
			})
			t.Run(enc+"/"+rf.name+"/json", func(t *testing.T) {
				r := observeRig(t)
				text := r.observeCall(rf, []string{":status", "200", "content-type", "application/json", "content-encoding", enc}, [][]byte{encode(t, enc, []byte(rf.jsonOut))})
				if text != "The purple elephant is here." {
					t.Fatalf("OnReply saw %q", text)
				}
			})
		}
	}
}

// Observe: a request body over the recording buffer's 4 MiB is still read
// whole for OnRequest.
func TestV2ObserveLargeRequest(t *testing.T) {
	r := newV2Rig(t, false)
	big := strings.Repeat("x", 5<<20)
	body := `{"model":"m","messages":[{"role":"user","content":"` + big + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"I'm a silly rabbit"}]}`
	r.srv.handle(r.st, eReqHeaders(false, anthropicCall...))
	for i := 0; i < len(body); i += 1 << 20 {
		end := min(i+1<<20, len(body))
		r.srv.handle(r.st, eReqBody(body[i:end], end == len(body)))
	}
	r.ic.Close()
	calls, reqs, _ := r.fake.Snapshot()
	if strings.Join(calls, ",") != "start,request" || len(reqs) != 1 || reqs[0].GetMessages()[0].GetParts()[0].GetText().GetText() != "I'm a silly rabbit" {
		t.Fatalf("calls %v", calls)
	}
	if st := r.ic.Stats(); st.Unparsed != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// Over the Interceptor's own cap it can't be read, and is counted.
func TestV2ObserveRequestOverTheCap(t *testing.T) {
	r := newV2Rig(t, false)
	r.srv.handle(r.st, eReqHeaders(false, anthropicCall...))
	chunk := strings.Repeat("x", 1<<20)
	for i := 0; i <= interceptBodyCap>>20; i++ {
		r.srv.handle(r.st, eReqBody(chunk, false))
	}
	r.srv.handle(r.st, eReqBody("", true))
	r.ic.Close()
	if st := r.ic.Stats(); st.UnparsedReasons["over the buffer"] != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// The recording buffer keeps its own cap: only a stream that goes to an
// Interceptor holds more.
func TestRequestBufferCapWithoutInterceptor(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 64})
	srv := New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s"})
	st := &streamState{correlationID: "c"}
	srv.handle(st, eReqHeaders(false, anthropicCall...))
	srv.handle(st, eReqBody(strings.Repeat("x", 5<<20), false))
	if len(st.reqBuf) != reqBufferCap || !st.reqTrunc {
		t.Fatalf("buffered %d", len(st.reqBuf))
	}
	proc.Close()
}

// A final event the response buffer's cap cuts through is kept whole: Codex's
// response.completed repeats the turn's output, so on a large reply it
// straddles the cap, and its usage was lost.
func TestCodexFinalEventStraddlingTheCap(t *testing.T) {
	evs := []string{`{"type":"response.created","response":{"id":"resp_1","model":"gpt-x"}}`}
	for range 6 {
		evs = append(evs, `{"type":"response.output_text.delta","delta":"`+strings.Repeat("y", 60_000)+`"}`)
	}
	evs = append(evs, `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-x","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"`+
		strings.Repeat("y", 300_000)+`"}]}],"usage":{"input_tokens":700,"output_tokens":80}}}`)
	body := sse(true, evs...)
	if at := bytes.Index(body, []byte("event: response.completed")); at >= respBufferCap || len(body) <= respBufferCap {
		t.Fatalf("response.completed at %d of %d: the test needs it across the %d-byte cap", at, len(body), respBufferCap)
	}
	c := httpCall(t, "chatgpt.com", "/backend-api/codex/responses", body)
	if c.InputTokens != 700 || c.OutputTokens != 80 || c.StopReason != "completed" {
		t.Fatalf("usage lost: in %d out %d stop %q", c.InputTokens, c.OutputTokens, c.StopReason)
	}
}
