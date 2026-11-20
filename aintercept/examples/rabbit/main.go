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
// Fast mode (Anthropic's "speed": "fast") can be refused too, per person,
// without Interceptor content: with INTERCEPT_FAST_MODE=refuse, a fast
// request gets a 400 with INTERCEPT_FAST_MODE_MESSAGE (a default if unset),
// unless it's from a verified email in INTERCEPT_FAST_MODE_ALLOW
// (comma-separated). Unset, fast mode goes through as before.
//
// Run it and point an observer at it (INTERCEPTOR_ENDPOINT=<host>:9100,
// INTERCEPTOR_CONTENT=true). It listens on INTERCEPT_LISTEN (default :9100).
package main

import (
	"context"
	"log"
	"os"
	"regexp"
	"strings"

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

// defaultFastMessage is what a refused fast request says without
// INTERCEPT_FAST_MODE_MESSAGE.
const defaultFastMessage = "Fast mode is turned off by your organization's AI policy. Switch back to standard speed to continue."

// fastPolicy refuses fast mode, but for the verified emails in allow.
type fastPolicy struct {
	refuse  bool
	message string
	allow   map[string]bool
}

func fastPolicyFromEnv() fastPolicy {
	f := fastPolicy{refuse: os.Getenv("INTERCEPT_FAST_MODE") == "refuse", message: os.Getenv("INTERCEPT_FAST_MODE_MESSAGE")}
	if f.message == "" {
		f.message = defaultFastMessage
	}
	for _, e := range strings.Split(os.Getenv("INTERCEPT_FAST_MODE_ALLOW"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			if f.allow == nil {
				f.allow = map[string]bool{}
			}
			f.allow[e] = true
		}
	}
	return f
}

// answer refuses a fast request from anyone not allowed it, as Anthropic
// refuses one from an organization without fast mode (a 400): a person's
// email counts only when the provider verified it.
func (f fastPolicy) answer(o intercept.Options, p intercept.Person) intercept.Answer {
	if !f.refuse || o.Get(intercept.Speed) != "fast" || p.Verified && f.allow[strings.ToLower(p.Email)] {
		return nil
	}
	return intercept.Error(400, "invalid_request_error", f.message)
}

func rules(fast fastPolicy) []intercept.Item {
	return []intercept.Item{
		intercept.OnRequest(intercept.Prompt(intercept.Matches(rabbit))).Reply(tricks),
		intercept.OnRequest(intercept.Prompt(intercept.Matches(bing))).Reply(bong),
		intercept.Hooks{OnCallStart: func(_ context.Context, c *intercept.Call) intercept.Answer {
			return fast.answer(c.Options, c.Person)
		}},
	}
}

func main() { log.Fatal(intercept.Run(rules(fastPolicyFromEnv())...)) }
