package events

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractDeferred_ParsesModelTokensTools(t *testing.T) {
	req := []byte(`{"model":"claude-opus-4-8","tools":[{"name":"mcp__playwright__browser_navigate"}],"messages":[]}`)
	resp := []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":120}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n")
	e := Event{Phase: PhaseComplete}
	e.AttachBodies(req, resp, "", nil)
	e.ExtractDeferred()

	if e.Model != "claude-opus-4-8" {
		t.Fatalf("model: %q", e.Model)
	}
	if e.InputTokens != 120 || e.OutputTokens != 42 {
		t.Fatalf("tokens: in=%d out=%d", e.InputTokens, e.OutputTokens)
	}
	if len(e.ToolsAvailable) != 1 || e.ToolsAvailable[0].Server != "playwright" {
		t.Fatalf("tools: %+v", e.ToolsAvailable)
	}
}

func TestClearBuffers_DropsRawBuffers(t *testing.T) {
	// ExtractDeferred no longer clears the buffers itself (the drain goroutine
	// needs them for unrecognized-shape sampling AFTER extraction); clearBuffers
	// does the drop. Verify the buffers survive extraction and are gone after
	// clearBuffers.
	e := Event{Phase: PhaseComplete}
	e.AttachBodies([]byte(`{"model":"m"}`), nil, "", map[string]string{"content-type": "application/json"})
	e.ExtractDeferred()
	if e.rawReq == nil {
		t.Fatal("ExtractDeferred must NOT clear raw buffers (sampling runs after it)")
	}
	e.clearBuffers()
	if e.rawReq != nil || e.rawResp != nil || e.reqHeaders != nil || e.respHeaders != nil || e.deferExtract {
		t.Fatal("raw buffers / headers / flag not cleared by clearBuffers")
	}
}

func TestExtractDeferred_NoAttachIsNoop(t *testing.T) {
	e := Event{Phase: PhaseComplete, Model: "preset"}
	e.ExtractDeferred() // nothing attached
	if e.Model != "preset" {
		t.Fatal("no-op extract mutated the event")
	}
}

// The security-critical invariant: a durable/network sink view must carry NO
// content — not the full body, not the bounded preview, and not the raw buffers
// (which are unexported and never serialize anyway).
func TestWithoutContent_StripsAllContentFields(t *testing.T) {
	e := Event{
		Phase:       PhaseResponseBodyChunk,
		BodyB64:     "c2VjcmV0IHByb21wdA==", // "secret prompt"
		BodyPreview: "secret prompt preview",
		Model:       "claude-opus-4-8",
		InputTokens: 10,
	}
	e.AttachBodies([]byte(`{"secret":"prompt"}`), []byte(`{"secret":"response"}`), "", nil)

	stripped := e.WithoutContent()
	if stripped.BodyB64 != "" || stripped.BodyPreview != "" {
		t.Fatalf("content leaked: b64=%q preview=%q", stripped.BodyB64, stripped.BodyPreview)
	}
	// Structured metadata survives.
	if stripped.Model != "claude-opus-4-8" || stripped.InputTokens != 10 {
		t.Fatal("WithoutContent wrongly dropped metadata")
	}
	// Serialized form must not contain any of the secret content. (Use distinctive
	// secret tokens, not words like "response" that legitimately appear in field
	// names like phase="response_body_chunk".)
	b, _ := json.Marshal(stripped)
	for _, needle := range []string{"c2VjcmV0", `"secret"`, "prompt preview"} {
		if bytes.Contains(b, []byte(needle)) {
			t.Fatalf("serialized stripped event contains %q: %s", needle, b)
		}
	}
}

func TestExtractDeferred_TruncatedGzipSalvagesInputTokens(t *testing.T) {
	// A response over the cap is a truncated gzip; we should still recover the
	// input_tokens from the early message_start frame rather than losing all usage.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":200}}}\n\n" +
		strings.Repeat("data: {\"type\":\"ping\"}\n\n", 50)))
	gw.Close()
	full := buf.Bytes()
	truncated := full[:len(full)-20] // chop the gzip tail

	e := Event{Phase: PhaseComplete}
	e.AttachBodies(nil, truncated, "gzip", nil)
	e.ExtractDeferred()
	if e.InputTokens != 200 {
		t.Fatalf("expected salvaged input_tokens=200 from truncated gzip, got %d", e.InputTokens)
	}
}
