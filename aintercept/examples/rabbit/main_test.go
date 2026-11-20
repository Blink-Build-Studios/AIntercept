package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept/intercepttest"
	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// The phrases, from fragments.
var (
	sillyRabbit = "I'm a sil" + "ly rab" + "bit"
	firstBing   = "fir" + "st we bi" + "ng"
)

func TestRabbit(t *testing.T) {
	h := intercepttest.New(rules(fastPolicy{})...)
	h.Prompt(sillyRabbit+"!").WantReply(t, tricks)
	h.Prompt("I’m a SIL"+"LY RAB"+"BIT").WantReply(t, tricks)
	h.Prompt("well, "+firstBing).WantReply(t, bong)
	h.ToolResult("Read", "the file says "+sillyRabbit).WantAccepted(t) // prompts only, not tool results
	h.Prompt("hello").WantAccepted(t)
	// Nothing on the reply: it goes as it came, and so do tool calls.
	h.Reply("I'm a sil", "ly rab", "bit").WantClientSees(t, sillyRabbit)
	h.ToolCall("Bash", `{"command":"echo `+firstBing+`"}`).WantAccepted(t)
}

func fastStart(id, email string, verified, fast bool) *pb.CallStart {
	s := &pb.CallStart{Call: &pb.Call{CallId: id, Mode: pb.Mode_MODE_ENFORCE, Identity: &pb.Identity{Email: email}},
		Request: &pb.RequestInfo{Model: "m"}}
	if verified {
		s.Call.Identity.EmailSource = pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL
	}
	if fast {
		s.Request.Options = []*pb.OptionValue{{Key: pb.Option_OPTION_SPEED, Value: "fast"}}
	}
	return s
}

// With INTERCEPT_FAST_MODE=refuse, a fast request is refused with the
// message, unless the provider verified its email is allowed; a standard one
// always goes on. Unset, fast mode goes on.
func TestFastMode(t *testing.T) {
	t.Setenv("INTERCEPT_FAST_MODE", "refuse")
	t.Setenv("INTERCEPT_FAST_MODE_ALLOW", " Ada@Example.com ,")
	s := intercept.New(rules(fastPolicyFromEnv())...)
	for _, tc := range []struct {
		email          string
		verified, fast bool
		refused        bool
	}{
		{"bob@example.com", true, true, true},
		{"ada@example.com", true, true, false},
		{"ada@example.com", false, true, true}, // the client says so: not proof
		{"bob@example.com", true, false, false},
	} {
		v, err := s.OnCallStart(context.Background(), fastStart(tc.email+fmt.Sprint(tc.verified, tc.fast), tc.email, tc.verified, tc.fast))
		if err != nil {
			t.Fatal(err)
		}
		if e := v.GetError(); (e != nil) != tc.refused || tc.refused && (e.GetHttpStatus() != 400 || e.GetMessage() != defaultFastMessage) {
			t.Errorf("%+v: %v", tc, v)
		}
	}
	off := intercept.New(rules(fastPolicy{})...)
	if v, _ := off.OnCallStart(context.Background(), fastStart("off", "bob@example.com", true, true)); v.GetError() != nil {
		t.Errorf("refused with the policy off: %v", v)
	}
}
