package main

import (
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept/intercepttest"
)

// The phrases, from fragments.
var (
	sillyRabbit = "I'm a sil" + "ly rab" + "bit"
	firstBing   = "fir" + "st we bi" + "ng"
)

func TestRabbit(t *testing.T) {
	h := intercepttest.New(rules()...)
	h.Prompt(sillyRabbit+"!").WantReply(t, tricks)
	h.Prompt("I’m a SIL"+"LY RAB"+"BIT").WantReply(t, tricks)
	h.Prompt("well, "+firstBing).WantReply(t, bong)
	h.ToolResult("Read", "the file says "+sillyRabbit).WantAccepted(t) // prompts only, not tool results
	h.Prompt("hello").WantAccepted(t)
	// Nothing on the reply: it goes as it came, and so do tool calls.
	h.Reply("I'm a sil", "ly rab", "bit").WantClientSees(t, sillyRabbit)
	h.ToolCall("Bash", `{"command":"echo `+firstBing+`"}`).WantAccepted(t)
}
