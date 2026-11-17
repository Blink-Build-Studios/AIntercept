package records

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

func saidOf(t *testing.T, host, path, body string) *events.CallDetail {
	t.Helper()
	r, ok := convo.Parse(host, path, []byte(body))
	if !ok {
		t.Fatalf("not a conversation: %s", body)
	}
	d := &events.CallDetail{}
	said(d, r)
	return d
}

func claude(t *testing.T, body string) *events.CallDetail {
	return saidOf(t, "api.anthropic.com", "/v1/messages", body)
}

// A turn's text is its request's new messages, the harness's pieces typed by
// convo's markers; the prompt is the person's words alone.
func TestSaidTypesTheHarnessPieces(t *testing.T) {
	d := claude(t, `{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"ok"},
		{"role":"user","content":[{"type":"text","text":"<system-reminder>be brief</system-reminder>"},{"type":"text","text":"fix the flaky test"}]}]}`)
	want := []events.SaidPart{{Role: "system", Note: "<system-reminder", Text: "<system-reminder>be brief</system-reminder>"},
		{Role: "user", Text: "fix the flaky test"}}
	if !reflect.DeepEqual(d.Said, want) || d.Prompt != "fix the flaky test" || d.PromptChars != 18 || d.Interrupted || d.Side != "" {
		t.Fatalf("said %+v", d)
	}
}

// A turn opened by a system message (a background task's notice) has its text
// as a typed part, and no words of the person's.
func TestSaidSystemOpenedTurn(t *testing.T) {
	d := claude(t, `{"messages":[{"role":"user","content":"<system-reminder>the background task ended</system-reminder>"}]}`)
	if len(d.Said) != 1 || d.Said[0].Role != "system" || d.Prompt != "" {
		t.Fatalf("said %+v", d)
	}
}

// The harness's interrupt note says the turn before was interrupted (Claude
// Code's, with a tool's result or the next prompt; Codex's <turn_aborted>).
func TestSaidInterrupt(t *testing.T) {
	d := claude(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"},{"type":"text","text":"[Request interrupted by user for tool use]"},{"type":"text","text":"no, stop"}]}]}`)
	if !d.Interrupted || d.Prompt != "no, stop" {
		t.Fatalf("interrupt: %+v", d)
	}
	d = saidOf(t, "chatgpt.com", "/backend-api/codex/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<turn_aborted>x</turn_aborted>"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"try again"}]}]}`)
	if !d.Interrupted || d.Prompt != "try again" {
		t.Fatalf("codex interrupt: %+v", d)
	}
	d = claude(t, `{"messages":[{"role":"user","content":"[Request interrupted by user]"},{"role":"assistant","content":"ok"},{"role":"user","content":"go on"}]}`)
	if d.Interrupted || d.Prompt != "go on" {
		t.Fatalf("an old note isn't new: %+v", d)
	}
}

// Claude Code's title and suggestion side requests, by their notes.
func TestSaidSideRequests(t *testing.T) {
	if d := claude(t, `{"messages":[{"role":"user","content":"<session>name it</session>"}]}`); d.Side != "title" {
		t.Errorf("title: %+v", d)
	}
	if d := claude(t, `{"messages":[{"role":"user","content":"[SUGGESTION MODE: next prompt]"}]}`); d.Side != "suggestion" {
		t.Errorf("suggestion: %+v", d)
	}
}

// The prompt's whole length beside its start.
func TestSaidPromptChars(t *testing.T) {
	long := strings.Repeat("é", 2005)
	d := claude(t, `{"messages":[{"role":"user","content":"`+long+`"}]}`)
	if d.PromptChars != 2005 || d.Prompt == long {
		t.Fatalf("prompt chars %d", d.PromptChars)
	}
}
