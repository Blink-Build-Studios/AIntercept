package process

import (
	"encoding/json"
	"strconv"
)

// What the streaming processor's session rows (internal/stream/state) read of
// one call or one event: the same parsing the reference's session rows use
// (sessionrows.go), exported so both read records alike.

// SessionRL is a call's Anthropic rate-limit headers (personmap.go rateLimit).
type SessionRL struct {
	At      int64
	Status  string
	Util    *float64
	Overage *string
}

// SessionCall is what the session rows read of one call.
type SessionCall struct {
	ID      string // call_id with '#' written '~'
	TS      int64  // start, else end
	Agent   bool   // agent_calls: a coding agent's model call with a session
	Visible bool   // cc_calls: … not hidden

	Product, Client, ClientVersion, Hidden, Effort string
	Text                                           *string // detail_json's text
	Prompt, ReqID, ClientReqID, Person, Device, IP string
	Input, Output, CacheRead, CacheWrite           int64
	HasTools                                       bool
	ToolCalls                                      int64
	CLI                                            *string
	RL                                             *SessionRL
	Ext                                            string  // a Codex call's ext_json, when it names workspaces
	Sandbox                                        *string // a Codex call's ext_json sandbox_mode
	Provider                                       string  // a Codex call's provider_json with x-codex-* headers
}

func sessionCallOf(c *rowCall) SessionCall {
	x := SessionCall{ID: c.id, TS: c.ts, Agent: c.agentCall(), Visible: c.ccCall(),
		Product: c.product, Client: c.client, ClientVersion: c.clientVersion, Hidden: c.hidden, Effort: c.effort,
		Text: c.text, Prompt: c.prompt, ReqID: c.reqID, ClientReqID: c.clientReqID, Person: c.person, Device: c.device,
		IP: c.ip, Input: c.input, Output: c.output, CacheRead: c.cacheRead, CacheWrite: c.cacheWrite,
		HasTools: c.hasTools, ToolCalls: c.toolCalls, CLI: c.cli, Ext: c.ext, Sandbox: c.sandbox, Provider: c.provider}
	if c.rl != nil {
		x.RL = &SessionRL{At: c.rl.at, Status: c.rl.status, Util: c.rl.util, Overage: c.rl.overage}
	}
	return x
}

// SessionCallOf is an ended request as the session rows read it.
func SessionCallOf(r Request) SessionCall {
	c := callFromRequest(colsOf(r), interner{})
	return sessionCallOf(&c)
}

// SessionPendingOf is a call in flight as the session rows read it.
func SessionPendingOf(p Pending) SessionCall {
	c := callFromPending(p)
	return sessionCallOf(&c)
}

// SessionLineage folds an ended request into its session's origin, as the
// sessions snapshot does: a model call's time into FirstNS, a Codex call's
// lineage into the origin fields. False when it isn't a model call.
func SessionLineage(s *Session, r Request) bool {
	if !isModelCall(&r) {
		return false
	}
	at := r.StartNS
	if at == 0 {
		at = r.EndNS
	}
	if r.Product == productCodex {
		s.addLineage(lineageOf(r.ExtJSON))
	}
	s.FirstNS = minNZ(s.FirstNS, at)
	return true
}

// SessionOrigin is a session row's origin columns from its sessions snapshot row.
func SessionOrigin(o *Session) (kind, sessionID, oj *string) {
	if o == nil || !(o.OriginKind != "" || o.Init == "cold_resume") {
		return nil, nil, nil
	}
	k := o.OriginKind
	if k == "" {
		k = "resume"
	}
	kind, sessionID = ptr(k), nz(o.OriginSessionID)
	switch k {
	case "fork", "resume", "subagent", "review":
		j := originJSON{Kind: k, SessionID: nz(o.OriginSessionID), AgentName: nz(o.AgentName)}
		if o.OriginTurn != 0 {
			j.Turn = ptr(o.OriginTurn)
		}
		switch o.Init {
		case "warm_fork", "cold_fork", "cold_resume":
			j.Init = ptr(o.Init)
		}
		oj = ptr(marshalJSON(j))
	}
	return
}

// SessionChildrenJSON is a session row's children_json from its children's
// (session, kind, first time), in that order already.
func SessionChildrenJSON(ids, kinds []string) *string {
	if len(ids) == 0 {
		return nil
	}
	cj := childrenJSON{SessionIDs: []string{}}
	for i, id := range ids {
		if kinds[i] == "subagent" {
			cj.Subagent++
		} else {
			cj.Review++
		}
		cj.SessionIDs = append(cj.SessionIDs, id)
	}
	return ptr(marshalJSON(cj))
}

// SessionEvent is what the session rows read of one analytics event.
type SessionEvent struct {
	CC   bool // a Claude Code event of a session: counted, in the span
	Turn bool // a Codex codex_turn_event of a session
	NS   int64
	Key  string

	Person   string
	Device   string // when one of sessionEventNames names it
	Env      [3]string
	EnvOK    [3]bool
	Query    bool // tengu_api_query with a permission mode
	Bypass   bool // … bypassPermissions
	Negative bool
	Cancel   bool
	Destroy  bool
	Sandbox  *string
	CustomCA bool

	Prompt      bool // tengu_input_prompt
	PromptKey   string
	PromptIndex *int64
	Report      bool // tengu_api_success
	ReqID       string
	ClientReqID string
	TurnMD      string
}

// SessionEventOf is an event as the session rows read it.
func SessionEventOf(e Event) SessionEvent {
	x := SessionEvent{NS: e.NS, Key: e.EventKey}
	if e.Source == productCodex && e.Name == "codex_turn_event" && e.SessionID != "" {
		x.Turn, x.TurnMD = e.MetadataJSON != "", e.MetadataJSON
		return x
	}
	if e.Source != productClaudeCode || e.SessionID == "" {
		return x
	}
	x.CC, x.Person = true, e.PersonID
	if e.DeviceID != "" && sessionEventNames[e.Name] {
		x.Device = e.DeviceID
	}
	if e.EnvJSON != "" {
		md := jsonFields(e.EnvJSON, envKeys[:]...)
		for i, k := range envKeys {
			x.Env[i], x.EnvOK[i] = jsonString(md[k])
		}
	}
	switch e.Name {
	case "tengu_api_query":
		if m, _ := jsonString(jsonFields(e.MetadataJSON, "permissionMode")["permissionMode"]); m != "" {
			x.Query, x.Bypass = true, m == "bypassPermissions"
		}
	case "tengu_input_prompt":
		md := jsonFields(e.MetadataJSON, "is_negative", "cc_prompt_id", "prompt_index")
		if v, _ := jsonString(md["is_negative"]); v == "true" {
			x.Negative = true
		}
		x.Prompt = true
		pid, _ := jsonString(md["cc_prompt_id"])
		if pid == "" {
			pid = "input:" + strconv.FormatInt(e.NS, 10)
		}
		x.PromptKey, x.PromptIndex = pid, jsonInt(md["prompt_index"], -1<<31, 1<<31-1)
	case "tengu_cancel":
		x.Cancel = true
	case "tengu_bash_tool_command_executed":
		if v, _ := jsonString(jsonFields(e.MetadataJSON, "destructive_category")["destructive_category"]); v != "" && v != "none" {
			x.Destroy = true
		}
	case "tengu_api_success":
		md := jsonFields(e.MetadataJSON, "requestId", "clientRequestId")
		x.Report = true
		x.ReqID, _ = jsonString(md["requestId"])
		x.ClientReqID, _ = jsonString(md["clientRequestId"])
	}
	if s := newEventSums(e.Name, e.MetadataJSON); s != nil {
		x.Sandbox, x.CustomCA = s.sandbox, s.gzipCA
	}
	return x
}

// CodexSessionFacts is a Codex thread's workspace and facts from its newest
// ext_json, sandbox_mode and codex_turn_event metadata (sessionrows.go codexFacts).
func CodexSessionFacts(ext string, sandbox *string, turnMD string) (repo, commit, cwd, approval, sb *string, has bool) {
	if ext != "" {
		ws := pyObject(ext)["workspaces"]
		if key, val, ok := pyFirstField(ws); ok {
			cwd = ptr(key)
			if key != "" {
				repo = ptr(pyBasename(key))
				if sha := pyObject(string(val))["latest_git_commit_hash"]; len(sha) > 0 && sha[0] == '"' {
					var s string
					if json.Unmarshal(sha, &s) == nil {
						commit = ptr(pyPrefix(s, 7))
					}
				}
			}
		}
	}
	md := pyObject(turnMD)
	var sandboxPolicy *string
	if v := md["approval_policy"]; len(v) > 0 && v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			approval = &s
		}
	}
	if v := md["sandbox_policy"]; len(v) > 0 && v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			sandboxPolicy = &s
		}
	}
	sb = sandbox
	if sb == nil || *sb == "" {
		sb = sandboxPolicy
	}
	if cwd != nil || approval != nil || sb != nil {
		return repo, commit, cwd, approval, sb, true
	}
	return repo, commit, nil, nil, nil, false
}

// CodexLimits is agents.codex_limits of one response's provider_json at ts.
func CodexLimits(providerJSON string, ts int64) *string { return codexLimits(providerJSON, ts) }

// StripTitle is a title call's answer as the API keeps it.
func StripTitle(s string) string { return pyStripTitle(s) }

// SessionLabel is rows.label: the thread's title, else its id.
func SessionLabel(id string, title *string) (string, string) { return sessionLabel(id, title) }

// AgentOf is the agent a product's sessions show.
func AgentOf(product string) string { return agentOf(product) }

// CoverageWindows is claude_code.py windows over the missed reports' times.
func CoverageWindows(ts []int64) (from, to, count []int64) {
	return coverageWindows(append([]int64(nil), ts...))
}

// TitleWithin is how long after a Codex thread starts a title thread can name it.
const TitleWithin = titleWithin

// ProductCodex and ProductClaudeCode name the agents' products.
const (
	ProductCodex      = productCodex
	ProductClaudeCode = productClaudeCode
)
