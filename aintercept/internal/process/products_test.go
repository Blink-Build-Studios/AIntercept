package process

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// A Codex thread rolls up like a Claude Code session: its turns are prompts,
// prewarm and title calls are hidden, and a subagent thread names its parent.
func TestCodexRollup(t *testing.T) {
	ctx := context.Background()
	c := &capture{files: map[string]*rawFile{}}
	at := func(m int) int64 { return ns(t0.Add(time.Duration(m) * time.Minute)) }
	client := func(originator string) map[string]string {
		return map[string]string{"originator": originator, "user-agent": originator + "/0.164.0 (Debian 12.0.0; aarch64) unknown"}
	}
	turn := func(conn string, n, m int, md map[string]any, prewarm bool) {
		c.add("pod-a", events.Event{
			CorrelationID: conn, Phase: events.PhaseComplete, TimestampNS: at(m), Host: "chatgpt.com", Method: "GET",
			Path: "/backend-api/codex/responses", Status: 101, Transport: events.TransportWebSocket, WSTurn: n,
			Client: client("codex_exec"), AccountUUID: "acct-c", DeviceID: "inst-1", SessionID: md["thread_id"].(string),
			Model: "gpt-6.1-sol", InputTokens: 100, OutputTokens: 10, Prewarm: prewarm, ClientMetadata: md,
		})
	}
	root := func(kind, turnID string) map[string]any {
		return map[string]any{"thread_id": "th-root", "turn_id": turnID, "request_kind": kind, "thread_source": "user", "history_initialization": "new", "agent_name": "/root"}
	}
	turn("ws-1", 1, 1, root("prewarm", ""), true)
	turn("ws-1", 2, 2, root("turn", "turn-1"), false)
	turn("ws-1", 3, 3, root("turn", "turn-1"), false)
	turn("ws-1", 4, 4, map[string]any{"thread_id": "th-root", "turn_id": "", "thread_source": "thread_title"}, false)
	turn("ws-2", 1, 5, map[string]any{
		"thread_id": "th-child", "turn_id": "turn-c1", "request_kind": "turn", "thread_source": "subagent",
		"parent_thread_id": "th-root", "parent_turn_id": "turn-1", "subagent_kind": "thread_spawn",
		"history_initialization": "warm_fork", "agent_name": "/root/edge_tests",
	}, false)
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	now := t0.Add(2 * time.Hour)
	p := newProc(raw, out, &now, 1, 1000)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	sessions := map[string]Session{}
	for _, s := range readAll[Session](t, out, []string{p.man.Snapshots["sessions"]}) {
		sessions[s.SessionID] = s
	}
	r := sessions["th-root"]
	if r.Product != "codex" || r.Client != "exec" || r.ClientVersion != "0.164.0" || r.Calls != 2 || r.HiddenCalls != 2 ||
		r.Prompts != 1 || r.InputTokens != 200 || r.PersonID != "acct-c" || r.OriginKind != "" {
		t.Fatalf("root thread: %+v", r)
	}
	ch := sessions["th-child"]
	if ch.OriginKind != "subagent" || ch.OriginSessionID != "th-root" || ch.OriginTurnID != "turn-1" ||
		ch.Init != "warm_fork" || ch.AgentName != "/root/edge_tests" || ch.Calls != 1 {
		t.Fatalf("child thread: %+v", ch)
	}
	hidden := map[string]int{}
	for _, q := range readAll[Request](t, out, p.man.Facts["requests"]) {
		hidden[q.HiddenKind]++
	}
	if hidden["prewarm"] != 1 || hidden["title"] != 1 {
		t.Fatalf("hidden calls: %v", hidden)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, c := range [][3]string{
		{"2.1.9", "2.1.10", "2.1.10"}, {"2.1.10", "2.1.9", "2.1.10"}, {"", "0.164.0", "0.164.0"},
		{"0.167.0", "0.164.0", "0.167.0"}, {"2.1", "2.1.1", "2.1.1"}, {"1.0.0-beta", "1.0.0", "1.0.0-beta"},
	} {
		if got := newerVersion(c[0], c[1]); got != c[2] {
			t.Errorf("newerVersion(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// With full interception the raw rows carry the bodies, and a model call's row
// keeps what they show; without them it keeps nothing of the content.
func TestCallDetailFromRecordedBodies(t *testing.T) {
	ctx := context.Background()
	script := `await tools.exec_command({cmd: "npm test"});`
	item, _ := json.Marshal(map[string]any{"type": "custom_tool_call", "call_id": "call_2", "name": "exec", "input": script})
	resp := "data: " + `{"type":"response.output_item.done","item":` + string(item) + "}\n\n"
	req := `{"input":[{"type":"reasoning"},{"type":"custom_tool_call","call_id":"call_1"},` +
		`{"type":"custom_tool_call_output","call_id":"call_1","output":"{\"exit_code\":1}"}]}`
	for _, recorded := range []bool{true, false} {
		c := &capture{files: map[string]*rawFile{}}
		b64 := func(s string) string {
			if !recorded {
				return ""
			}
			return base64.StdEncoding.EncodeToString([]byte(s))
		}
		row := func(seq int, phase events.Phase, body string) events.Event {
			return events.Event{CorrelationID: "post-1", Seq: uint64(seq), TimestampNS: ns(t0.Add(time.Duration(seq) * time.Second)),
				Phase: phase, Host: "chatgpt.com", Method: "POST", Path: "/backend-api/codex/responses", BodyB64: b64(body), Bytes: len(body)}
		}
		c.add("pod-a", row(1, events.PhaseRequestHeaders, ""))
		c.add("pod-a", row(2, events.PhaseRequestBodyChunk, req))
		c.add("pod-a", row(3, events.PhaseResponseHeaders, ""))
		c.add("pod-a", row(4, events.PhaseResponseBodyChunk, resp))
		done := row(5, events.PhaseComplete, "")
		done.Status, done.Client = 200, map[string]string{"originator": "codex_exec", "user-agent": "codex_exec/0.164.0"}
		c.add("pod-a", done)
		raw, out := newMem(), newMem()
		for _, f := range c.sorted() {
			put(t, raw, f)
		}
		now := t0.Add(time.Hour)
		p := newProc(raw, out, &now, 10, 1000)
		if err := p.Open(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Poll(ctx, true); err != nil {
			t.Fatal(err)
		}
		reqs := readAll[Request](t, out, p.man.Facts["requests"])
		if len(reqs) != 1 {
			t.Fatalf("want one request row, got %d", len(reqs))
		}
		var d events.CallDetail
		if recorded {
			if err := json.Unmarshal([]byte(reqs[0].DetailJSON), &d); err != nil || len(d.Calls) != 1 ||
				len(d.Calls[0].Commands) != 1 || d.Calls[0].Commands[0] != "npm test" || len(d.Results["call_1"].Exit) != 1 {
				t.Fatalf("detail from recorded bodies: %q", reqs[0].DetailJSON)
			}
		} else if reqs[0].DetailJSON != "" {
			t.Fatalf("no bodies, yet a detail: %q", reqs[0].DetailJSON)
		}
	}
}
