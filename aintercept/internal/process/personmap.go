package process

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
)

// The person tables (docs/architecture.md, Decision 4): who each person is and
// what of theirs the people pages roll up, as the query API derived them
// (claude_code.py linked_accounts, email_groups, person_sessions,
// unattributed_sessions; person.py header_facts; directory.py).
//
//   - person_map: each person id's linked accounts (this one, and every account
//     with the same email), and which of them are the members of the person's
//     People-page row. A dimension: queries join it, fact rows never copy it.
//   - person_sessions: each person id's sessions (every session one of their
//     accounts appears on; "unattributed": the sessions none does).
//   - account_facts and account_devices: what an account's own events and calls
//     say about it (its org, plan, Codex plan; its devices and their environment).
//   - call_tool_uses: the tools each call's response invoked, by time, so a
//     summary over a range is SQL.
//
// What a person's pages show over a range (the summary, the People page's rows)
// is SQL over these and the per-session facts in session_rows, session_marks and
// event_rows; what depends on now (live) stays the API's.

// PersonMapRow is a person_map row: one of the person's linked accounts.
type PersonMapRow struct {
	PersonID  string `parquet:"person_id"`
	AccountID string `parquet:"account_id"`
	// Directory: the person id is a People-page row's (its email group's first
	// account with a session) and this account is one of the row's members (the
	// group's accounts with a session).
	Directory bool `parquet:"directory"`
}

// PersonSession is a person_sessions row.
type PersonSession struct {
	PersonID  string `parquet:"person_id"`
	SessionID string `parquet:"session_id"`
}

// AccountFact is an account_facts row: what an account's Claude Code events and
// Codex calls say about it.
type AccountFact struct {
	AccountID string  `parquet:"account_id"`
	Org       *string `parquet:"org,optional"`        // the earliest event's organization (the least, at the same time)
	Plan      *string `parquet:"plan,optional"`       // the newest event's subscription_type
	CodexPlan *string `parquet:"codex_plan,optional"` // the newest Codex call's x-codex-plan-type
}

// AccountDevice is an account_devices row: a device the account's calls or events
// name, with its environment from the account's newest events that carry it.
type AccountDevice struct {
	AccountID string  `parquet:"account_id"`
	DeviceID  string  `parquet:"device_id"`
	OnEvents  bool    `parquet:"on_events"` // an event of the account names it (the header's devices)
	Platform  *string `parquet:"platform,optional"`
	Terminal  *string `parquet:"terminal,optional"`
	Shell     *string `parquet:"shell,optional"`
}

// CallToolUse is a call_tool_uses row: a tool a call's response invoked (its
// tool_uses_json; person.py tool_uses), kind "mcp" (with its server) or "builtin".
type CallToolUse struct {
	SessionID string `parquet:"session_id"`
	TsNS      int64  `parquet:"ts_ns"`
	Kind      string `parquet:"kind"`
	Server    string `parquet:"server"`
	Tool      string `parquet:"tool"`
}

// rateLimit is a call's Anthropic unified rate-limit headers (person.py header_facts).
type rateLimit struct {
	at      int64 // coalesce(response_start_ns, ts)
	status  string
	util    *float64
	overage *string
}

var cliVersion = regexp.MustCompile(`claude-cli/([^ ]+)`)

// personFacts keeps what the person rollups read of a visible model call: its
// Claude Code version (regexp_extract of a claude-cli user-agent) and its
// rate-limit headers.
func (c *rowCall) personFacts(ua, provider string, at int64) {
	if strings.HasPrefix(ua, "claude-cli/") {
		v := ""
		if m := cliVersion.FindStringSubmatch(ua); m != nil {
			v = m[1]
		}
		c.cli = ptr(strings.Clone(v))
	}
	if strings.Contains(provider, "anthropic-ratelimit-unified-status") {
		h := jsonFields(provider, "anthropic-ratelimit-unified-status", "anthropic-ratelimit-unified-7d-utilization",
			"anthropic-ratelimit-unified-overage-status")
		status, ok := jsonString(h["anthropic-ratelimit-unified-status"])
		if !ok {
			return
		}
		rl := &rateLimit{at: at, status: strings.Clone(status), util: jsonDouble(h["anthropic-ratelimit-unified-7d-utilization"])}
		if o, ok := jsonString(h["anthropic-ratelimit-unified-overage-status"]); ok {
			rl.overage = ptr(strings.Clone(o))
		}
		c.rl = rl
	}
}

// newest is the newest value seen (the greater one at the same time), so it
// doesn't depend on the order events are read in.
type newest struct {
	ts int64
	v  string
	ok bool
}

func (p *newest) offer(ts int64, v string) {
	if !p.ok || ts > p.ts || ts == p.ts && v > p.v {
		*p = newest{ts, v, true}
	}
}

func (p newest) merge(o newest) newest {
	if o.ok {
		p.offer(o.ts, o.v)
	}
	return p
}

func (p newest) val() *string {
	if !p.ok {
		return nil
	}
	return ptr(p.v)
}

// acctEvents is what an account's Claude Code events say about it.
type acctEvents struct {
	org     string // the earliest event's (the least, at the same time)
	orgNS   int64
	plan    newest
	devices map[string]*[3]newest // device -> platform, terminal, shell
}

func (a *eventAgg) acctOf(acct string) *acctEvents {
	x := a.accts[acct]
	if x == nil {
		x = &acctEvents{devices: map[string]*[3]newest{}}
		a.accts[acct] = x
	}
	return x
}

// addAccount folds in one of an account's Claude Code events.
func (x *acctEvents) add(e eventCols) {
	if earlier(e.NS, e.OrgID, x.orgNS, x.org) {
		x.org, x.orgNS = strings.Clone(e.OrgID), e.NS
	}
	if strings.Contains(e.MetadataJSON, "subscription_type") && (!x.plan.ok || e.NS >= x.plan.ts) {
		if v, ok := jsonString(jsonFields(e.MetadataJSON, "subscription_type")["subscription_type"]); ok {
			x.plan.offer(e.NS, strings.Clone(v))
		}
	}
	if e.DeviceID == "" {
		return
	}
	env := x.devices[e.DeviceID]
	if env == nil {
		env = &[3]newest{}
		x.devices[e.DeviceID] = env
	}
	if e.EnvJSON == "" {
		return
	}
	stale := true
	for _, p := range env {
		if !p.ok || e.NS >= p.ts {
			stale = false
		}
	}
	if stale {
		return
	}
	md := jsonFields(e.EnvJSON, envKeys[:]...)
	for i, k := range envKeys {
		if v, ok := jsonString(md[k]); ok {
			env[i].offer(e.NS, strings.Clone(v))
		}
	}
}

func (x *acctEvents) merge(o *acctEvents) {
	if earlier(o.orgNS, o.org, x.orgNS, x.org) {
		x.org, x.orgNS = o.org, o.orgNS
	}
	x.plan = x.plan.merge(o.plan)
	for d, env := range o.devices {
		cur := x.devices[d]
		if cur == nil {
			cur = &[3]newest{}
			x.devices[d] = cur
		}
		for i := range env {
			cur[i] = cur[i].merge(env[i])
		}
	}
}

// Derived person tables.
type personTables struct {
	Map      []PersonMapRow
	Sessions []PersonSession
	Facts    []AccountFact
	Devices  []AccountDevice
	Tools    []CallToolUse
}

// personTables builds the person tables. ids is every person id the API can be
// asked for (the request rows' people).
func (d *rowData) personTables(ids []string) personTables {
	var out personTables

	// Each account's sessions (claude_code.py person_sessions: a visible call or
	// a Claude Code event names it), and the accounts that own one.
	owned := map[string]bool{}
	for a, ss := range d.byAccount {
		if len(ss) > 0 {
			owned[a] = true
		}
	}
	// Email groups (claude_code.py email_groups): accounts with the same email.
	groupOf := map[string][]string{}
	{
		by := map[string]map[string]bool{}
		for _, a := range d.accounts {
			if a.Email != "" {
				addTo(by, strings.ToLower(a.Email), a.AccountID)
			}
		}
		for _, set := range by {
			if len(set) < 2 {
				continue
			}
			g := sortedKeys(set)
			for _, a := range g {
				groupOf[a] = g
			}
		}
	}
	// The People page's rows (directory.py people): each email group's accounts
	// that own a session, under the first of them.
	row := map[string][]string{}
	for _, a := range sortedKeys(owned) {
		g := []string{a}
		if grp, ok := groupOf[a]; ok {
			g = nil
			for _, x := range grp {
				if owned[x] {
					g = append(g, x)
				}
			}
		}
		if g[0] == a {
			row[a] = g
		}
	}

	all := map[string]bool{}
	for _, id := range ids {
		all[id] = true
	}
	for a := range d.byAccount {
		all[a] = true
	}
	for _, id := range sortedKeys(all) {
		var sids map[string]bool
		if id == unattributed {
			sids = d.unattributedSessions()
		} else {
			linked := d.linkedAccounts(id)
			sort.Strings(linked)
			members := map[string]bool{}
			for _, m := range row[id] {
				members[m] = true
			}
			for _, a := range linked {
				out.Map = append(out.Map, PersonMapRow{PersonID: id, AccountID: a, Directory: members[a]})
			}
			sids = d.personSessions(linked)
		}
		for _, s := range sortedKeys(sids) {
			out.Sessions = append(out.Sessions, PersonSession{PersonID: id, SessionID: s})
		}
	}

	// What each account's events and Codex calls say about it.
	codexPlan := map[string]*newest{}
	callDevices := map[string]map[string]bool{}
	for _, c := range d.calls {
		if c.person == "" || !c.agentCall() {
			continue
		}
		if c.ccCall() && c.device != "" {
			addTo(callDevices, c.person, c.device)
		}
		if c.product == productCodex && c.provider != "" {
			if v, ok := jsonString(jsonFields(c.provider, "x-codex-plan-type")["x-codex-plan-type"]); ok {
				p := codexPlan[c.person]
				if p == nil {
					p = &newest{}
					codexPlan[c.person] = p
				}
				p.offer(c.ts, v)
			}
		}
	}
	accts := map[string]bool{}
	for a := range d.accts {
		accts[a] = true
	}
	for a := range codexPlan {
		accts[a] = true
	}
	for a := range callDevices {
		accts[a] = true
	}
	for _, a := range sortedKeys(accts) {
		f := AccountFact{AccountID: a}
		devs := map[string]bool{}
		for dev := range callDevices[a] {
			devs[dev] = true
		}
		x := d.accts[a]
		if x != nil {
			f.Org, f.Plan = nz(x.org), x.plan.val()
			for dev := range x.devices {
				devs[dev] = true
			}
		}
		if p := codexPlan[a]; p != nil {
			f.CodexPlan = p.val()
		}
		out.Facts = append(out.Facts, f)
		for _, dev := range sortedKeys(devs) {
			r := AccountDevice{AccountID: a, DeviceID: dev}
			if x != nil {
				if env := x.devices[dev]; env != nil {
					r.OnEvents = true
					r.Platform, r.Terminal, r.Shell = env[0].val(), env[1].val(), env[2].val()
				}
			}
			out.Devices = append(out.Devices, r)
		}
	}

	// The tools each ended call's response invoked.
	seen := map[CallToolUse]bool{}
	for _, c := range d.calls {
		if !c.ended || c.session == "" {
			continue
		}
		for _, u := range c.toolUses {
			var k CallToolUse
			switch {
			case u.kind == "mcp" && u.server != "" && u.tool != "":
				k = CallToolUse{SessionID: c.session, TsNS: c.ts, Kind: "mcp", Server: u.server, Tool: u.tool}
			case u.tool != "":
				k = CallToolUse{SessionID: c.session, TsNS: c.ts, Kind: "builtin", Tool: u.tool}
			default:
				continue
			}
			if !seen[k] {
				seen[k] = true
				out.Tools = append(out.Tools, k)
			}
		}
	}
	sort.Slice(out.Tools, func(i, j int) bool {
		a, b := out.Tools[i], out.Tools[j]
		switch {
		case a.SessionID != b.SessionID:
			return a.SessionID < b.SessionID
		case a.TsNS != b.TsNS:
			return a.TsNS < b.TsNS
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.Server != b.Server:
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
	return out
}

// eventSums is what the person summary sums or counts of an event, read as the
// API's SQL read it (json_extract_string, then TRY_CAST): nil for the events it
// reads nothing of.
type eventSums struct {
	tool     *string // tengu_tool_use_success: toolName
	mcp      bool    // … isMcp is 'true'
	hookMS   *float64
	argv0    *string // tengu_bash_tool_command_executed: bash_argv0,
	category *string // destructive_category,
	outside  bool    // destructive_target_scope is 'outside_cwd'
	hook     *string // tengu_run_hook: hookName
	gitOp    *string // tengu_git_operation: operation
	pr       *string // tengu_session_linked_to_pr: prNumber
	cache5m  *int64  // tengu_api_success: cache_creation_5m_input_tokens,
	cache1h  *int64  // cache_creation_1h_input_tokens,
	costUSD  *float64
	sandbox  *string // tengu_bash_tool_command_executed: sandbox_enabled
	gzipCA   bool    // tengu_api_success: gzipSkipReason is 'custom_ca'
}

// jsonText is json_extract_string: nil for null or absent.
func jsonText(v json.RawMessage) *string {
	if s, ok := jsonString(v); ok {
		return &s
	}
	return nil
}

func newEventSums(name, md string) *eventSums {
	switch name {
	case "tengu_tool_use_success":
		f := jsonFields(md, "toolName", "isMcp", "preToolHookDurationMs")
		mcp, _ := jsonString(f["isMcp"])
		return &eventSums{tool: jsonText(f["toolName"]), mcp: mcp == "true", hookMS: jsonDouble(f["preToolHookDurationMs"])}
	case "tengu_bash_tool_command_executed":
		f := jsonFields(md, "bash_argv0", "destructive_category", "destructive_target_scope", "sandbox_enabled")
		scope, _ := jsonString(f["destructive_target_scope"])
		return &eventSums{argv0: jsonText(f["bash_argv0"]), category: jsonText(f["destructive_category"]),
			outside: scope == "outside_cwd", sandbox: jsonText(f["sandbox_enabled"])}
	case "tengu_run_hook":
		return &eventSums{hook: jsonText(jsonFields(md, "hookName")["hookName"])}
	case "tengu_git_operation":
		return &eventSums{gitOp: jsonText(jsonFields(md, "operation")["operation"])}
	case "tengu_session_linked_to_pr":
		return &eventSums{pr: jsonText(jsonFields(md, "prNumber")["prNumber"])}
	case "tengu_api_success":
		f := jsonFields(md, "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens", "costUSD", "gzipSkipReason")
		gz, _ := jsonString(f["gzipSkipReason"])
		return &eventSums{
			cache5m: jsonInt(f["cache_creation_5m_input_tokens"], math.MinInt64, math.MaxInt64),
			cache1h: jsonInt(f["cache_creation_1h_input_tokens"], math.MinInt64, math.MaxInt64),
			costUSD: jsonDouble(f["costUSD"]), gzipCA: gz == "custom_ca"}
	}
	return nil
}
