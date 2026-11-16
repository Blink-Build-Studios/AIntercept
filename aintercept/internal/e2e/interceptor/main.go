// The Interceptor the end-to-end suite (internal/e2e, contract v2) runs: the
// example rabbit's rules on the request, plus rules that prove the reply's
// paths: tool calls held and answered or replaced, and reply text replaced
// even when the phrase is split across streamed pieces. Built on package
// intercept. Not shipped: test/interceptor's compose builds it.
package main

import (
	"log"
	"regexp"

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
		intercept.OnRequest(intercept.Matches(rabbit)).Reply(tricks),
		intercept.OnRequest(intercept.Matches(bing)).Reply(bong),
		intercept.OnToolCallRequest("", intercept.Matches(bing)).Reply(bong),
		intercept.OnToolCallRequest("", intercept.Matches(rabbit)).Replace(tricks),
		intercept.OnResponse(intercept.Contains("swordfish")).Replace(password),
	}
}

func main() { log.Fatal(intercept.Run(Rules()...)) }
