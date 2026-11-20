package main

import (
	"context"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept/intercepttest"
	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// Redacted wherever it appears, in both directions.
func TestRedactsBothWays(t *testing.T) {
	h := intercepttest.New(items()...)
	h.Prompt("hello "+phrase).WantReplaced(t, "hello "+redacted)
	h.Prompt("A "+"SIL"+"LY CLO"+"WN!").WantReplaced(t, "A "+redacted+"!")
	h.ToolResult("Read", "the file says "+phrase).WantReplaced(t, "the file says "+redacted)
	h.Harness("<system-reminder>"+phrase+"</system-reminder>").WantReplaced(t, "<system-reminder>"+redacted+"</system-reminder>")
	h.Prompt("hello").WantAccepted(t)
	// The reply, even split across streamed pieces, and a tool call's
	// arguments.
	h.Reply("what a ", "sil", "ly clo", "wn indeed").WantClientSees(t, "what a "+redacted+" indeed")
	h.Reply("nothing to see").WantClientSees(t, "nothing to see")
	h.ToolCall("Bash", `{"command":"echo `+phrase+`"}`).WantReplaced(t, `{"command":"echo `+redacted+`"}`)
}

// The forbidden model is answered with the refusal at OnCallStart: the call
// never reaches it. Any other model goes on.
func TestRefusesTheForbiddenModel(t *testing.T) {
	s := intercepttest.New(items()...).Server()
	start := func(model string) *pb.CallVerdict {
		v, err := s.OnCallStart(context.Background(), &pb.CallStart{
			Call: &pb.Call{CallId: model, Mode: pb.Mode_MODE_ENFORCE}, Request: &pb.RequestInfo{Model: model}})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := start(forbidden).GetRespond().GetText(); got != refusal {
		t.Errorf("forbidden-model: answered %q, want %q", got, refusal)
	}
	if v := start("claude-sonnet"); v.GetAction() != nil {
		t.Errorf("another model: %v, want accepted", v)
	}
}
