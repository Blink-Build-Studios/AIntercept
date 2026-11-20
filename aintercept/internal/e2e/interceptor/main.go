// The Interceptor the end-to-end suite (internal/e2e, contract v2) runs: the
// example rabbit's rules on the request, plus rules that prove the reply's
// paths: tool calls held and answered or replaced, and reply text replaced
// even when the phrase is split across streamed pieces; and options: fast
// mode and the priority tier refused with an Error, at call start and per
// WebSocket turn. Built on package intercept. Not shipped:
// test/interceptor's compose builds it.
package main

import (
	"context"
	"log"
	"regexp"
	"strconv"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
)

// The phrases, built from fragments (they mustn't appear whole in the
// source: the rules would answer anything that quotes them).
var (
	rabbit = regexp.MustCompile(`(?i)i['’]?m a sil` + `ly rab` + `bit`)
	bing   = regexp.MustCompile(`(?i)fir` + `st we bi` + `ng`)
)

const (
	tricks   = "Well, tricks are for kids."
	bong     = "then we bong"
	password = "[password]"
)

// Rules is the suite's rules.
func Rules() []intercept.Item {
	return []intercept.Item{
		intercept.OnRequest(intercept.Prompt(intercept.Matches(rabbit))).Reply(tricks),
		intercept.OnRequest(intercept.Prompt(intercept.Matches(bing))).Reply(bong),
		intercept.OnToolCallRequest("", intercept.Matches(bing)).Reply(bong),
		intercept.OnToolCallRequest("", intercept.Matches(rabbit)).Replace(tricks),
		intercept.OnResponse(intercept.Contains("swordfish")).Replace(password),
		// Who's calling: the identity the observer resolved, on the verdict row.
		intercept.OnRequest(intercept.Prompt(intercept.Contains("who am i"))).Do(func(m *intercept.Message) intercept.Answer {
			p := m.Call.Person
			return intercept.Accept().Note("email", p.Email).Note("account", p.Account).Note("verified", strconv.FormatBool(p.Verified))
		}),
	}
}

// FastNotEnabled is Anthropic's own refusal of fast mode, which Claude Code
// answers by retrying at standard speed.
const FastNotEnabled = "Fast mode is not enabled for your organization."

// refuse is the options rule: no fast mode, no priority tier.
func refuse(o intercept.Options) intercept.Answer {
	switch {
	case o.Get(intercept.Speed) == "fast":
		return intercept.Error(400, "invalid_request_error", FastNotEnabled)
	case o.Get(intercept.ServiceTier) == "priority":
		return intercept.Error(403, "", "The priority tier isn't enabled here.")
	}
	return nil
}

// Options is the suite's rule on options, at call start and per turn.
var Options = intercept.Hooks{
	OnCallStart: func(_ context.Context, c *intercept.Call) intercept.Answer { return refuse(c.Options) },
	OnTurnStart: func(_ context.Context, t *intercept.Turn) intercept.Answer { return refuse(t.Options) },
}

func main() { log.Fatal(intercept.Run(append(Rules(), Options)...)) }
