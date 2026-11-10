package events

import (
	"bytes"
	"compress/gzip"
	"runtime"
	"testing"

	"github.com/andybalholm/brotli"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.Bytes()
}

func TestModelFromRequestBody(t *testing.T) {
	m := ModelFromRequestBody([]byte(`{"model":"claude-sonnet-4-6","messages":[]}`))
	if m != "claude-sonnet-4-6" {
		t.Fatalf("got %q", m)
	}
	if ModelFromRequestBody([]byte(`not json`)) != "" {
		t.Fatal("expected empty for bad json")
	}
}

func TestUsage_AnthropicSSE(t *testing.T) {
	// message_start carries input + cache; message_delta carries final output.
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":120,"cache_read_input_tokens":40,"cache_creation_input_tokens":10}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":350}}` + "\n\n" +
		"data: [DONE]\n\n"
	u, ok := UsageFromResponseBody([]byte(sse), "")
	if !ok {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 120 || u.OutputTokens != 350 || u.CacheReadTokens != 40 || u.CacheWriteTokens != 10 {
		t.Fatalf("bad usage: %+v", u)
	}
}

func TestUsage_AnthropicSSE_Gzipped(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":7}}` + "\n\n"
	u, ok := UsageFromResponseBody(gz(t, sse), "gzip")
	if !ok {
		t.Fatal("expected usage from gzipped SSE")
	}
	if u.InputTokens != 5 || u.OutputTokens != 7 {
		t.Fatalf("bad usage: %+v", u)
	}
}

func TestUsage_OpenAINonStreamedJSON(t *testing.T) {
	body := `{"id":"x","model":"gpt-4","usage":{"prompt_tokens":200,"completion_tokens":80}}`
	u, ok := UsageFromResponseBody([]byte(body), "")
	if !ok {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 200 || u.OutputTokens != 80 {
		t.Fatalf("bad usage: %+v", u)
	}
}

func TestUsage_OutputTokensTakesMax(t *testing.T) {
	// Successive deltas: the parser keeps the largest output seen.
	sse := `data: {"usage":{"output_tokens":10}}` + "\n\n" +
		`data: {"usage":{"output_tokens":42}}` + "\n\n" +
		`data: {"usage":{"output_tokens":30}}` + "\n\n"
	u, _ := UsageFromResponseBody([]byte(sse), "")
	if u.OutputTokens != 42 {
		t.Fatalf("expected max output 42, got %d", u.OutputTokens)
	}
}

func TestUsage_NoUsageReturnsFalse(t *testing.T) {
	if _, ok := UsageFromResponseBody([]byte(`data: {"type":"ping"}`+"\n"), ""); ok {
		t.Fatal("expected no usage")
	}
}

func TestUsage_TruncatedGzipDoesNotPanic(t *testing.T) {
	good := gz(t, `data: {"usage":{"output_tokens":1}}`)
	// Chop the gzip stream — decode should fail gracefully, no usage, no panic.
	if _, ok := UsageFromResponseBody(good[:len(good)/2], "gzip"); ok {
		t.Fatal("expected no usage from truncated gzip")
	}
}

// Anthropic returns non-streaming JSON responses Brotli-compressed.
func TestUsage_AnthropicJSON_Brotli(t *testing.T) {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	_, _ = w.Write([]byte(`{"type":"message","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":31,"output_tokens":7,"cache_read_input_tokens":2}}`))
	_ = w.Close()
	u, ok := UsageFromResponseBody(buf.Bytes(), "br")
	if !ok || u.InputTokens != 31 || u.OutputTokens != 7 || u.CacheReadTokens != 2 {
		t.Fatalf("got %+v ok=%v", u, ok)
	}
}

func TestDecodeBodyZstdHugeWindowDoesNotAllocate(t *testing.T) {
	// A frame header claiming a 512 MiB window (descriptor 0x98), one empty
	// last raw block. Unbounded, the decoder allocates the window up front.
	bomb := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x98, 0x01, 0x00, 0x00}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _ = decodeBody(bomb, "zstd")
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("decoding a 9-byte zstd frame allocated %d MiB", grew>>20)
	}
}
