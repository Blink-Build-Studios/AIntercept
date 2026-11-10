package events

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSinkWritesNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "events.ndjson")
	fs, err := NewFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	fs.Emit(Event{CorrelationID: "a", Phase: PhaseRequestHeaders})
	fs.Emit(Event{CorrelationID: "b", Phase: PhaseComplete})
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	f, _ := os.Open(path)
	defer f.Close()
	var lines int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %d not valid JSON: %v", lines, err)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d", lines)
	}
}

// This is the safety-critical test: full body content must reach the durable
// sink but NEVER the stdout-safe sink.
func TestMultiSinkStripsFullBodyFromStdout(t *testing.T) {
	stdout := &captureSink{}
	durable := &captureSink{}
	ms := NewMultiSink([]Sink{stdout}, []Sink{durable})

	secret := "FULL PROMPT CONTENT: my api key is sk-ant-xyz and here is my data"
	ms.Emit(Event{
		CorrelationID: "x",
		Phase:         PhaseRequestBodyChunk,
		BodyPreview:   "truncated...",
		BodyB64:       secret, // stand-in for base64 body content
	})

	if len(durable.got) != 1 || durable.got[0].BodyB64 != secret {
		t.Fatalf("durable sink must receive full body; got %+v", durable.got)
	}
	if len(stdout.got) != 1 {
		t.Fatalf("stdout sink should receive the event, got %d", len(stdout.got))
	}
	if stdout.got[0].BodyB64 != "" {
		t.Fatalf("SECURITY: full body leaked to stdout sink: %q", stdout.got[0].BodyB64)
	}
	// Preview may remain on stdout (that's the capped, opt-in field).
	if stdout.got[0].BodyPreview != "truncated..." {
		t.Fatalf("stdout should keep the capped preview, got %q", stdout.got[0].BodyPreview)
	}
}

type captureSink struct{ got []Event }

func (c *captureSink) Emit(e Event) error { c.got = append(c.got, e); return nil }
func (c *captureSink) Flush() error       { return nil }

func TestForStdoutDoesNotMutateOriginal(t *testing.T) {
	e := Event{BodyB64: "secret"}
	safe := e.ForStdout()
	if safe.BodyB64 != "" {
		t.Fatal("ForStdout must strip BodyB64")
	}
	if e.BodyB64 != "secret" {
		t.Fatal("ForStdout must not mutate the original event")
	}
	_ = strings.TrimSpace
}
