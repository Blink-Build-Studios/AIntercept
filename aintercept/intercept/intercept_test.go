package intercept_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept/intercepttest"
	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

var apiKey = regexp.MustCompile(`sk-[A-Za-z0-9]{8,}`)

// The rules in the package doc, end to end.
func rules() []intercept.Item {
	return []intercept.Item{
		intercept.OnRequest(intercept.Contains("magic word")).Reply("No magic here."),
		intercept.OnMessage(intercept.Matches(apiKey)).Replace("[secret]"),
		intercept.OnResponse(intercept.Contains("swordfish")).Replace("[password]"),
		intercept.OnToolCallRequest("Bash", intercept.Arg("command", intercept.Contains("rm -rf"))).Reply("I won't run that."),
		intercept.OnToolCallResponse("Bash", intercept.Contains("BEGIN PRIVATE KEY")).Replace("[redacted]"),
	}
}

func TestRules(t *testing.T) {
	h := intercepttest.New(rules()...)
	h.Prompt("say the MAGIC WORD please").WantReply(t, "No magic here.")
	h.Prompt("hello").WantAccepted(t)
	h.Prompt("my key is sk-abcdefgh123 ok").WantReplaced(t, "my key is [secret] ok")
	h.ToolCall("Bash", `{"command":"rm -rf /"}`).WantReply(t, "I won't run that.")
	h.ToolCall("Bash", `{"command":"ls"}`).WantAccepted(t)
	h.ToolCall("Read", `{"file_path":"rm -rf"}`).WantAccepted(t)
	h.ToolResult("Bash", "-----BEGIN PRIVATE KEY-----\nabc").WantReplaced(t, "-----[redacted]-----\nabc")
	h.ToolResult("Read", "BEGIN PRIVATE KEY").WantAccepted(t)
	// The key in a tool call's arguments: replaced inside the string value.
	h.ToolCall("Bash", `{"command":"echo sk-abcdefgh123"}`).WantReplaced(t, `{"command":"echo [secret]"}`)
}

// Prompt: the user's prompts only. A tool result (a file the agent read)
// saying the same thing doesn't match, nor does the model's reply.
func TestPromptMatcher(t *testing.T) {
	h := intercepttest.New(
		intercept.OnRequest(intercept.Prompt(intercept.Contains("magic word"))).Reply("No magic here."),
		intercept.OnMessage(intercept.Prompt(intercept.Matches(apiKey))).Replace("[secret]"),
	)
	h.Prompt("say the magic word").WantReply(t, "No magic here.")
	h.ToolResult("Read", "the file says: magic word").WantAccepted(t)
	h.Prompt("key sk-abcdefgh123").WantReplaced(t, "key [secret]")
	h.ToolResult("Bash", "key sk-abcdefgh123").WantAccepted(t)
	h.Reply("the magic word").WantClientSees(t, "the magic word")
	h.ToolCall("Bash", `{"command":"echo magic word"}`).WantAccepted(t)
	h2 := intercepttest.New(intercept.OnRequest(intercept.Prompt(nil)).Reply("every prompt"))
	h2.Prompt("anything").WantReply(t, "every prompt")
	h2.ToolResult("Read", "anything").WantAccepted(t)
}

// A rule on reply text is a watch: the phrase split across pieces is caught;
// the text before it isn't held back.
func TestRulesOnReplyText(t *testing.T) {
	h := intercepttest.New(rules()...)
	h.Reply("The password is sword", "fi", "sh!").WantClientSees(t, "The password is [password]!")
	h.Reply("The password is SWORDFISH").WantClientSees(t, "The password is [password]")
	h.Reply("a sword", " is not a fish").WantClientSees(t, "a sword is not a fish")
	h.Reply("key: sk-abc", "defgh123 done").WantClientSees(t, "key: [secret] done")
	h.Reply("nothing", " here").WantClientSees(t, "nothing here")
}

func TestWatchReply(t *testing.T) {
	h := intercepttest.New(intercept.Hooks{Watches: []intercept.Watch{{
		Phrase: "magic word", In: intercept.FromModel,
		On: func(_ context.Context, w *intercept.Match) intercept.Answer { return w.Reply("Stopped.") },
	}}})
	h.Reply("Say the ma", "gic word now").WantClientSees(t, "Say the Stopped.")
	h.Prompt("magic word").WantAccepted(t) // FromModel only
}

func TestWatchPattern(t *testing.T) {
	h := intercepttest.New(intercept.Watch{
		Pattern: regexp.MustCompile(`\d{3}-\d{4}`), MaxLen: 8,
		On: func(_ context.Context, w *intercept.Match) intercept.Answer { return w.Replace("###-####") },
	})
	h.Reply("call 555-", "1234 now").WantClientSees(t, "call ###-#### now")
	h.Prompt("call 555-1234").WantReplaced(t, "call ###-####") // both directions by default
}

// Every piece reaches OnResponse and OnMessage, and can be changed, with no
// watches at all.
func TestHooksSeeEveryPiece(t *testing.T) {
	var pieces []string
	h := intercepttest.New(intercept.Hooks{
		OnResponse: func(_ context.Context, m *intercept.Message) intercept.Answer {
			pieces = append(pieces, m.Before+"|"+m.Text)
			return intercept.Replace(strings.ToUpper(m.Text))
		},
	})
	h.Reply("one ", "two").WantClientSees(t, "ONE TWO")
	if strings.Join(pieces, ",") != "|one ,ONE |two" {
		t.Fatalf("OnResponse saw %q", pieces)
	}
}

// Hold: wait for more, then decide on it all.
func TestHookHold(t *testing.T) {
	h := intercepttest.New(intercept.Hooks{OnResponse: func(_ context.Context, m *intercept.Message) intercept.Answer {
		if !m.Final && !strings.HasSuffix(m.Text, ".") {
			return intercept.Hold()
		}
		return intercept.Replace("[" + m.Text + "]")
	}})
	h.Reply("a ", "b", ".").WantClientSees(t, "[a b.]")
}

// Most specific first; the first answer that isn't Accept wins.
func TestDispatchOrder(t *testing.T) {
	var order []string
	hook := func(name string, a intercept.Answer) func(context.Context, *intercept.Message) intercept.Answer {
		return func(context.Context, *intercept.Message) intercept.Answer { order = append(order, name); return a }
	}
	h := intercepttest.New(intercept.Hooks{
		OnToolCallResponse: func(context.Context, *intercept.ToolResult) intercept.Answer {
			order = append(order, "OnToolCallResponse")
			return nil
		},
		OnToolCallRequest: func(_ context.Context, tc *intercept.ToolCall) intercept.Answer {
			order = append(order, "OnToolCallRequest:"+tc.Command())
			return nil
		},
		OnRequest:  hook("OnRequest", nil),
		OnResponse: hook("OnResponse", intercept.Accept()),
		OnMessage:  hook("OnMessage", intercept.Reply("from OnMessage")),
	})
	h.ToolResult("Bash", "ok").WantReply(t, "from OnMessage")
	h.ToolCall("shell", `{"command":["bash","-lc","ls -la"]}`).WantReply(t, "from OnMessage")
	h.Prompt("hi").WantReply(t, "from OnMessage")
	want := "OnToolCallResponse,OnRequest,OnMessage,OnToolCallRequest:ls -la,OnResponse,OnMessage,OnRequest,OnMessage"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("order\n%s\nwant\n%s", got, want)
	}
	order = nil
	h2 := intercepttest.New(intercept.OnRequest(intercept.Contains("hi")).Reply("rule"), intercept.Hooks{OnRequest: hook("OnRequest", nil)})
	h2.Prompt("hi").WantReply(t, "rule")
	if len(order) != 0 {
		t.Fatalf("hooks ran after a rule answered: %v", order)
	}
}

// Fail and a panic are errors: the observer's onError applies.
func TestFail(t *testing.T) {
	h := intercepttest.New(intercept.Hooks{
		OnRequest:  func(context.Context, *intercept.Message) intercept.Answer { return intercept.Fail(errors.New("no")) },
		OnResponse: func(context.Context, *intercept.Message) intercept.Answer { panic("boom") },
	})
	h.Prompt("x").WantFailed(t)
	h.Reply("x").WantFailed(t)
	h.ToolCall("Bash", `{}`).WantFailed(t)
}

// Nothing on the reply: the observer is told not to hold it (UNIMPLEMENTED),
// and it passes as it came.
func TestNothingOnReplies(t *testing.T) {
	h := intercepttest.New(intercept.OnRequest(intercept.Contains("x")).Reply("y"))
	if _, err := h.Server().OnReply(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "Unimplemented") {
		t.Fatalf("OnReply: %v", err)
	}
	if _, err := h.Server().OnToolCall(context.Background(), nil); err == nil {
		t.Fatalf("OnToolCall: %v", err)
	}
	h.Reply("x").WantClientSees(t, "x")
}

// The same Call in every hook of one call; per-call state.
func TestCallState(t *testing.T) {
	var seen []*intercept.Call
	h := intercepttest.New(intercept.Hooks{
		OnCallStart: func(_ context.Context, c *intercept.Call) intercept.Answer {
			c.Set("k", "v")
			seen = append(seen, c)
			return nil
		},
		OnRequest: func(_ context.Context, m *intercept.Message) intercept.Answer {
			seen = append(seen, m.Call)
			if m.Call.Get("k") != "v" || !m.Newest || !m.IsPrompt() || !m.Changeable() || m.Call.Agent != intercept.ClaudeCode {
				return intercept.Fail(errors.New("state"))
			}
			return nil
		},
	})
	h.Prompt("hi").WantAccepted(t)
	if len(seen) != 2 || seen[0] != seen[1] {
		t.Fatalf("calls %v", seen)
	}
}

func TestToolCallHelpers(t *testing.T) {
	var got []string
	h := intercepttest.New(intercept.Hooks{OnToolCallRequest: func(_ context.Context, tc *intercept.ToolCall) intercept.Answer {
		got = append(got, tc.Name, tc.Server, tc.Command(), tc.Path(), tc.Arg("n"))
		return intercept.ReplaceArgs(`{"command":"pwd"}`)
	}})
	h.ToolCall("mcp__files__read", `{"path":"/a","n":3}`).WantReplaced(t, `{"command":"pwd"}`)
	if strings.Join(got, ",") != "mcp__files__read,files,,/a,3" {
		t.Fatalf("got %q", got)
	}
}

// Notes go with the answer.
func TestNotes(t *testing.T) {
	h := intercepttest.New(intercept.Hooks{OnRequest: func(context.Context, *intercept.Message) intercept.Answer {
		return intercept.Accept().Note("seen", "yes")
	}})
	h.Prompt("hi").WantAccepted(t)
}

// NeedFull on a truncated message: the observer fetches it, and the call
// comes again with the whole content.
func TestNeedFull(t *testing.T) {
	var seen []string
	s := intercept.New(intercept.Hooks{OnRequest: func(_ context.Context, m *intercept.Message) intercept.Answer {
		seen = append(seen, m.Text)
		if m.Truncated {
			return intercept.NeedFull()
		}
		if strings.Contains(m.Text, "needle") {
			return intercept.Reply("found")
		}
		return nil
	}})
	req := &pb.RequestMessages{Call: &pb.Call{CallId: "c"}, Messages: []*pb.ConvMessage{{Id: "m", Role: pb.Role_ROLE_USER, Parts: []*pb.Part{
		{Id: "m/0", Ref: true, Size: 100000, Preview: "hay hay", Kind: &pb.Part_Text{Text: &pb.Text{}}}}}}}
	v, err := s.OnRequest(context.Background(), req)
	if err != nil || len(v.GetFetch().GetPartIds()) != 1 || v.GetFetch().GetPartIds()[0] != "m/0" {
		t.Fatalf("%v %v", v, err)
	}
	p := req.Messages[0].Parts[0]
	p.Ref, p.Preview, p.GetText().Text, req.Fetched = false, "", "hay hay needle", true
	if v, err = s.OnRequest(context.Background(), req); err != nil || v.GetRespond().GetText() != "found" {
		t.Fatalf("%v %v", v, err)
	}
	if strings.Join(seen, "|") != "hay hay|hay hay needle" {
		t.Fatalf("seen %q", seen)
	}
}

// Observe: nothing is held, so the released text comes back as Before; a
// watch still finds a match that started in it, and the answer is recorded.
func TestWatchObserve(t *testing.T) {
	s := intercept.New(intercept.OnResponse(intercept.Contains("swordfish")).Replace("[password]"))
	call := &pb.Call{CallId: "c", Mode: pb.Mode_MODE_OBSERVE}
	piece := func(released, text string) *pb.MessageVerdict {
		v, err := s.OnReply(context.Background(), &pb.ResponseMessage{Call: call, Released: released,
			Message: &pb.ConvMessage{Id: "m:0", Parts: []*pb.Part{{Id: "m:0/0", Kind: &pb.Part_Text{Text: &pb.Text{Text: text}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	piece("", "The password is sword")
	if v := piece("The password is sword", "fish."); v.GetReplace().GetMessage().GetParts()[0].GetText().GetText() != "[password]." {
		t.Fatalf("observe: %v", v)
	}
}

// The harness's notes: OnRequest and OnMessage see them as KindHarness;
// rules and watches act on them, Replace and Reply work; Prompt never
// matches them.
func TestHarnessNotes(t *testing.T) {
	var kinds []string
	h := intercepttest.New(
		intercept.OnRequest(intercept.Prompt(intercept.Contains("magic word"))).Reply("No magic here."),
		intercept.OnRequest(intercept.Contains("internal-only")).Replace("[removed]"),
		intercept.Hooks{OnMessage: func(_ context.Context, m *intercept.Message) intercept.Answer {
			kinds = append(kinds, m.Kind.String())
			if m.Kind == intercept.KindHarness && strings.Contains(m.Text, "stop now") {
				return intercept.Reply("stopped")
			}
			return nil
		}},
	)
	h.Harness("<system-reminder>the magic word is in a file</system-reminder>").WantAccepted(t)
	h.Harness("Note: internal-only.txt changed on disk.").WantReplaced(t, "Note: [removed].txt changed on disk.")
	h.Harness("stop now").WantReply(t, "stopped")
	h.Prompt("the magic word").WantReply(t, "No magic here.")
	if strings.Join(kinds, ",") != "harness,harness" {
		t.Fatalf("OnMessage saw %v", kinds)
	}
}
