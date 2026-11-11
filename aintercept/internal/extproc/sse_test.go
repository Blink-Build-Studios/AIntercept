package extproc

import (
	"strings"
	"testing"
)

func TestSSESplitterAcrossChunks(t *testing.T) {
	var s sseSplitter
	in := "event: a\ndata: {\"x\":1}\n\nevent: b\r\ndata: line1\r\ndata: line2\r\n\r\ndata: tail"
	var got []sseEvent
	for i := 0; i < len(in); i += 7 { // 7-byte chunks: boundaries mid-line
		got = append(got, s.feed([]byte(in[i:min(i+7, len(in))]))...)
	}
	if len(got) != 2 || got[0].name != "a" || string(got[0].data) != `{"x":1}` || got[1].name != "b" || string(got[1].data) != "line1\nline2" {
		t.Fatalf("events %+v", got)
	}
	if r := string(s.rest()); r != "data: tail" {
		t.Fatalf("rest %q", r)
	}
	if string(got[0].raw)+string(got[1].raw) != in[:len(in)-len("data: tail")] {
		t.Fatal("raw bytes don't reassemble the stream")
	}
}

func TestSSEWithDataKeepsOtherFields(t *testing.T) {
	ev := parseSSE([]byte("id: 7\nevent: delta\ndata: old\ndata: more\nretry: 10\n\n"))
	got := string(ev.withData([]byte("new1\nnew2")))
	want := "id: 7\nevent: delta\ndata: new1\ndata: new2\nretry: 10\n\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !strings.HasSuffix(string(parseSSE([]byte(": comment\n\n")).withData([]byte("x"))), "data: x\n\n") {
		t.Fatal("event without data gets one")
	}
}
