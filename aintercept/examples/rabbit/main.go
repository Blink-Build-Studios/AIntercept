// rabbit is a minimal Interceptor (docs/BUILDING_AN_INTERCEPTOR_V2.md), on
// package intercept. When the user's prompt says the rabbit's phrase (the
// one that ends "…silly rabbit"), it answers for the model: "Well, tricks
// are for kids." When one says the bing phrase, it answers "then we bong".
// Tool results (a file the agent read, a command's output) aren't looked at.
// Everything else goes through.
//
// It sees each call's new messages only, in one shape for Claude Code, Codex
// (HTTP or WebSocket) and the OpenAI APIs: a phrase earlier in the
// conversation isn't seen again. Its answer reaches the client as the
// model's reply, in the provider's format. It has no rules on the model's
// reply, so the observer doesn't hold replies for it.
//
// Run it and point an observer at it (INTERCEPTOR_ENDPOINT=<host>:9100,
// INTERCEPTOR_CONTENT=true). It listens on INTERCEPT_LISTEN (default :9100).
package main

import (
	"log"
	"regexp"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
)

// The phrases (a straight or curly apostrophe, or none), built from
// fragments so this file doesn't say them whole.
var (
	rabbit = regexp.MustCompile(`(?i)i['’]?m a sil` + `ly rab` + `bit`)
	bing   = regexp.MustCompile(`(?i)fir` + `st we bi` + `ng`)
)

const (
	tricks = "Well, tricks are for kids."
	bong   = "then we bong"
)

func rules() []intercept.Item {
	return []intercept.Item{
		intercept.OnRequest(intercept.Prompt(intercept.Matches(rabbit))).Reply(tricks),
		intercept.OnRequest(intercept.Prompt(intercept.Matches(bing))).Reply(bong),
	}
}

func main() { log.Fatal(intercept.Run(rules()...)) }
