// redact is a minimal Interceptor (docs/BUILDING_AN_INTERCEPTOR_V2.md), on
// package intercept. It replaces the clown's phrase (the one that ends
// "…lly clown") with "[redacted]" wherever it appears, in both directions:
// the user's prompts, tool results and the harness's notes on the way to the
// model, and the model's reply text and tool calls on the way back. It
// answers a call that asks for the model "forbidden-model" with a refusal,
// and logs who makes each call and how it ended.
//
// It shows the rest of the contract beside examples/rabbit: a rule on every
// message both ways (on the reply's text it's a watch, so a phrase split
// across two streamed pieces is still caught), OnCallStart deciding from
// the call (its model, and who's calling), and OnCallComplete receiving the
// record.
//
// Run it and point an observer at it (INTERCEPTOR_ENDPOINT=<host>:9100,
// INTERCEPTOR_CONTENT=true), and put a host in enforce scope to see prompts
// redacted before they reach the provider. In observe mode the same answers
// are only recorded. It listens on INTERCEPT_LISTEN (default :9100).
package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
)

// The phrase, built from fragments so this file doesn't say it whole.
var phrase = "sil" + "ly clo" + "wn"

const (
	redacted  = "[redacted]"
	forbidden = "forbidden-model"
	refusal   = "forbidden-model isn't approved for use here."
)

func items() []intercept.Item {
	return []intercept.Item{
		intercept.OnMessage(intercept.Contains(phrase)).Replace(redacted),
		intercept.Hooks{OnCallStart: onCallStart, OnCallComplete: onCallComplete},
	}
}

// onCallStart logs who's calling and refuses the forbidden model. Verified
// means the provider confirmed the email for this call's credential;
// otherwise it's what the client says.
func onCallStart(_ context.Context, c *intercept.Call) intercept.Answer {
	slog.Info("call start", "call", c.ID, "mode", mode(c.Mode), "agent", string(c.Agent), "model", c.Model,
		"email", c.Person.Email, "verified", c.Person.Verified, "account", c.Person.Account)
	if c.Model == forbidden {
		return intercept.Reply(refusal)
	}
	return nil
}

func onCallComplete(_ context.Context, r *intercept.Record) {
	slog.Info("call complete", "call", r.Call.ID, "ws_turn", r.WSTurn, "status", r.Status, "model", r.Model,
		"in", r.Usage.Input, "out", r.Usage.Output, "duration_ms", r.Duration.Milliseconds(),
		"answers", len(r.Answers), "replied", r.Replied)
}

func mode(m intercept.Mode) string {
	if m == intercept.Enforce {
		return "enforce"
	}
	return "observe"
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	log.Fatal(intercept.Run(items()...))
}
