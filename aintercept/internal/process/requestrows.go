package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// The request_rows snapshot: every person's request log, as the query API
// showed it (api/src/aintercept_api/rows.py request_rows, over person.py and
// claude_code.py). Rebuilt from all the committed data each run: the requests
// parts, the in-flight transactions, the analytics events and the accounts. What
// each fact part gives is kept in memory across runs (parts never change), so a
// run decodes only its new parts; the rebuild itself is still over everything.
// The parity test (data/parity, docs/architecture.md Decision 4) holds it equal
// to the API's rows, field for field, on a real capture.
//
// Who a row belongs to (the API's rules, kept exactly):
//   - A person is an account, with every account that has the same email.
//     Their sessions are those any of the accounts appears on (a visible model
//     call or a Claude Code analytics event). "unattributed" is the sessions of
//     visible model calls no account appears on at all.
//   - Their credentials (key_hash) are those of the sessions' model calls, and
//     of the analytics batches that carried the sessions' Claude Code events.
//   - Their rows are every Claude Code or Codex transaction made with one of
//     those credentials (a WebSocket's turns, not the connection), plus each
//     tengu_api_success of their sessions no captured call matches (a call that
//     went around the proxy).

// unattributed is the person id of the sessions no account appears on (the API's
// claude_code.UNATTRIBUTED).
const unattributed = "unattributed"

// The observer parsed at most this much of a response before P6
// (docs/INTERCEPTION_ISSUES.md): a longer response lost the final events that carry its usage.
const parseBuffer = 512 * 1024

// retryAfterNS: tengu_api_retry is logged within seconds of the failed attempt's end.
const retryAfterNS = 30 * 1_000_000_000

// rowCall is one transaction as the API's `calls` view has it: an ended request,
// or an in-flight one from pending. Strings are "" and numbers 0 for null,
// as in the tables.
type rowCall struct {
	id     string // call_id with '#' written '~': the row's request_id
	corrID string // correlation_id: the transaction (a WebSocket's turns share it)
	ts     int64  // start, else end
	end    int64
	wsTurn int64

	host, method, path        string
	status                    int64
	synthetic                 bool
	keyHash, errText          string
	session, prompt, person   string
	class, clientReqID, reqID string
	model, stop               string

	input, output, cacheRead, cacheWrite int64
	up, down                             int64

	encoding, product, client, hidden, effort string
	tools                                     []string // tool_uses_json as labels
	text                                      *string  // detail_json's text

	// For the session tools (eventrows.go): an ended call's tool uses.
	ended    bool
	toolUses []toolUse

	// For the prompt rows (promptrows.go): a visible model call's step.
	agentType string
	does      string      // what the step did (the API's rows.step_kind)
	detail    *callDetail // detail_json, read; nil without one

	// For the session rows (sessionrows.go): a coding agent's model call.
	device, ip, clientVersion string
	toolCalls                 int64   // tool calls in tool_uses_json (rows.tool_calls' counts summed)
	hasTools                  bool    // tool_uses_json isn't empty
	ext                       string  // a Codex call's ext_json, when it names workspaces
	sandbox                   *string // a Codex call's ext_json sandbox_mode
	provider                  string  // a Codex call's provider_json, when it carries x-codex-* headers

	// For the person rollups (personmap.go): a visible model call's Claude Code
	// version (its user-agent's) and Anthropic rate-limit headers.
	cli *string
	rl  *rateLimit
}

// requestCols is the columns of a requests part the rows read.
type requestCols struct {
	CallID           string `parquet:"call_id"`
	CorrelationID    string `parquet:"correlation_id"`
	WSTurn           int64  `parquet:"ws_turn,optional"`
	StartNS          int64  `parquet:"start_ns,optional"`
	EndNS            int64  `parquet:"end_ns,optional"`
	Host             string `parquet:"host,optional"`
	Method           string `parquet:"method,optional"`
	Path             string `parquet:"path,optional"`
	Status           int64  `parquet:"status,optional"`
	Synthetic        bool   `parquet:"synthetic"`
	KeyHash          string `parquet:"key_hash,optional"`
	Error            string `parquet:"error,optional"`
	SessionID        string `parquet:"session_id,optional"`
	PromptID         string `parquet:"prompt_id,optional"`
	PersonID         string `parquet:"person_id,optional"`
	RequestClass     string `parquet:"request_class,optional"`
	ClientRequestID  string `parquet:"client_request_id,optional"`
	RequestID        string `parquet:"request_id,optional"`
	Model            string `parquet:"model,optional"`
	StopReason       string `parquet:"stop_reason,optional"`
	InputTokens      int64  `parquet:"input_tokens,optional"`
	OutputTokens     int64  `parquet:"output_tokens,optional"`
	CacheReadTokens  int64  `parquet:"cache_read_tokens,optional"`
	CacheWriteTokens int64  `parquet:"cache_write_tokens,optional"`
	RequestBytes     int64  `parquet:"request_bytes,optional"`
	ResponseBytes    int64  `parquet:"response_bytes,optional"`
	ContentEncoding  string `parquet:"content_encoding,optional"`
	ToolUsesJSON     string `parquet:"tool_uses_json,optional"`
	AgentType        string `parquet:"agent_type,optional"`
	Product          string `parquet:"product,optional"`
	Client           string `parquet:"client,optional"`
	HiddenKind       string `parquet:"hidden_kind,optional"`
	Effort           string `parquet:"effort,optional"`
	DetailJSON       string `parquet:"detail_json,optional"`
	DeviceID         string `parquet:"device_id,optional"`
	IP               string `parquet:"ip,optional"`
	ClientVersion    string `parquet:"client_version,optional"`
	ExtJSON          string `parquet:"ext_json,optional"`
	ProviderJSON     string `parquet:"provider_json,optional"`
	UserAgent        string `parquet:"user_agent,optional"`
	ResponseStartNS  int64  `parquet:"response_start_ns,optional"`
}

// eventCols is the columns of an events part the rows read.
type eventCols struct {
	CorrelationID string `parquet:"correlation_id"`
	RowNS         int64  `parquet:"row_ns"`
	NS            int64  `parquet:"ns"`
	Seq           int64  `parquet:"seq"`
	Source        string `parquet:"source"`
	Name          string `parquet:"name,optional"`
	SessionID     string `parquet:"session_id,optional"`
	PromptID      string `parquet:"prompt_id,optional"`
	PersonID      string `parquet:"person_id,optional"`
	MetadataJSON  string `parquet:"metadata_json,optional"`
	DeviceID      string `parquet:"device_id,optional"`
	EnvJSON       string `parquet:"env_json,optional"`
	HeadSHA       string `parquet:"head_sha,optional"`
	OrgID         string `parquet:"org_id,optional"`
	EventKey      string `parquet:"event_key"`
}

// callFromRequest is an ended request as the API's `calls` view has it. Its strings
// are copied (a decoded row's may share the part's buffers) and the repeated ones
// shared through in, since parts' calls are kept across runs.
func callFromRequest(r requestCols, in interner) rowCall {
	id := strings.Clone(strings.ReplaceAll(r.CallID, "#", "~"))
	corr := id
	if r.CorrelationID != id {
		corr = strings.Clone(r.CorrelationID)
	}
	c := rowCall{
		id: id, corrID: corr,
		ts: firstNZ(r.StartNS, r.EndNS), end: r.EndNS, wsTurn: r.WSTurn,
		host: in.of(r.Host), method: in.of(r.Method), path: in.of(r.Path), status: r.Status, synthetic: r.Synthetic,
		keyHash: in.of(r.KeyHash), errText: in.of(r.Error),
		session: in.of(r.SessionID), prompt: in.of(r.PromptID), person: in.of(r.PersonID),
		class: in.of(r.RequestClass), clientReqID: strings.Clone(r.ClientRequestID), reqID: strings.Clone(r.RequestID),
		model: in.of(r.Model), stop: in.of(r.StopReason),
		input: r.InputTokens, output: r.OutputTokens, cacheRead: r.CacheReadTokens, cacheWrite: r.CacheWriteTokens,
		up: r.RequestBytes, down: r.ResponseBytes,
		encoding: in.of(r.ContentEncoding), product: in.of(r.Product), client: in.of(r.Client),
		hidden: in.of(r.HiddenKind), effort: in.of(r.Effort),
	}
	for _, l := range toolLabels(r.ToolUsesJSON) {
		c.tools = append(c.tools, in.of(l))
	}
	c.ended = true
	if c.session != "" {
		c.toolUses = readToolUses(r.ToolUsesJSON, in)
	}
	if c.product == "" && c.host == "api.anthropic.com" { // rows from before products were tagged
		c.product = productClaudeCode
	}
	if r.DetailJSON != "" {
		var d map[string]any
		if json.Unmarshal([]byte(r.DetailJSON), &d) == nil {
			if s, ok := d["text"].(string); ok {
				c.text = &s
			}
		}
	}
	if c.ccCall() { // a step: what the prompt rows need of it
		c.agentType = in.of(r.AgentType)
		c.does = stepKind(r.ToolUsesJSON, c.stop)
		c.detail = readDetail(r.DetailJSON)
	}
	if c.agentCall() { // what the session rows need of it
		c.device, c.ip, c.clientVersion = in.of(r.DeviceID), in.of(r.IP), in.of(r.ClientVersion)
		c.hasTools, c.toolCalls = r.ToolUsesJSON != "", toolCount(r.ToolUsesJSON)
		c.agentExt(r.ExtJSON, r.ProviderJSON)
	}
	if c.ccCall() {
		c.personFacts(r.UserAgent, r.ProviderJSON, firstNZ(r.ResponseStartNS, c.ts))
	}
	return c
}

// agentExt keeps what the session rows read of a Codex call's ext_json and
// provider_json (the API's workspace, codex_facts and latest_codex_limits).
func (c *rowCall) agentExt(ext, provider string) {
	if c.product != productCodex {
		return
	}
	if strings.Contains(ext, "workspaces") {
		c.ext = strings.Clone(ext)
	}
	if v, ok := jsonString(jsonFields(ext, "sandbox_mode")["sandbox_mode"]); ok {
		c.sandbox = &v
	}
	if strings.Contains(provider, "x-codex-") {
		c.provider = strings.Clone(provider)
	}
}

// callFromPending is an in-flight transaction as the API's `calls` view has it:
// only what its headers say (the client values from its client_json).
func callFromPending(p Pending) rowCall {
	var h map[string]string
	if p.ClientJSON != "" {
		json.Unmarshal([]byte(p.ClientJSON), &h)
	}
	c := rowCall{
		id: p.CorrelationID, corrID: p.CorrelationID, ts: firstNZ(p.StartNS, p.SeenNS),
		host: p.Host, method: p.Method, path: p.Path, status: p.Status, synthetic: p.Synthetic, keyHash: p.KeyHash,
		session: h["x-claude-code-session-id"], prompt: h["x-claude-code-prompt-id"],
		class: h["x-claude-code-request-class"], clientReqID: h["x-client-request-id"], reqID: p.RequestID,
		agentType: h["x-claude-code-agent-type"], does: "text",
		up: p.RequestBytes, down: p.ResponseBytes, encoding: p.ContentEncoding, ip: p.IP,
	}
	switch {
	case p.Host == "api.anthropic.com":
		c.product = productClaudeCode
	case strings.HasPrefix(h["originator"], "codex"):
		c.product = productCodex
	}
	if c.agentCall() {
		c.agentExt("", p.ProviderJSON)
	}
	if c.ccCall() {
		c.personFacts(h["user-agent"], p.ProviderJSON, firstNZ(p.ResponseStartNS, c.ts))
	}
	return c
}

// agentCall is the API's agent_calls view: a coding agent's model call with a session.
func (c *rowCall) agentCall() bool {
	if c.synthetic || c.session == "" {
		return false
	}
	switch c.product {
	case productClaudeCode:
		return c.host == "api.anthropic.com" && strings.HasPrefix(c.path, "/v1/messages") &&
			!strings.HasPrefix(c.path, "/v1/messages/count_tokens")
	case productCodex:
		return c.host == "chatgpt.com" && c.path == "/backend-api/codex/responses" && (c.wsTurn != 0 || c.method == "POST")
	}
	return false
}

// ccCall is the API's cc_calls view: a model call the user sees.
func (c *rowCall) ccCall() bool { return c.hidden == "" && c.agentCall() }

// transaction is one the request log lists: a coding agent's, not a probe, and a
// WebSocket's turns rather than its connection.
func (c *rowCall) transaction() bool {
	return (c.product == productClaudeCode || c.product == productCodex) && !c.synthetic &&
		!(c.wsTurn == 0 && c.method == "GET" && c.path == "/backend-api/codex/responses")
}

// rowEvent is a Claude Code analytics event a row is built from: a call's report
// (tengu_api_success), a retry (tengu_api_retry) or a prompt (tengu_input_prompt).
// Its values are read as the API reads them (json_extract_string, then TRY_CAST).
type rowEvent struct {
	ts      int64
	key     string // event_key: breaks ties in time, so no pick depends on table order
	session string
	name    string
	// tengu_api_success
	reqID, clientReqID string
	ttft               *float64
	cache5m, cache1h   *int64
	// tengu_input_prompt and tengu_api_retry
	promptID    string
	hasPromptID bool // cc_prompt_id was present (a retry with none matches any prompt)
	index       *int64
	attempt     *int64
}

// batchOf is an analytics request's events (Claude Code's and Codex's).
type batchOf struct {
	n         int64
	session   string // the earliest event's that names one (the least, at the same time)
	sessionNS int64
	name      string // the least name
}

// earlier: (ns, v) comes before (curNS, cur), an unset cur ("") last. A pick by
// it doesn't depend on the order events are read in, which a restart changes.
func earlier(ns int64, v string, curNS int64, cur string) bool {
	return v != "" && (cur == "" || ns < curNS || ns == curNS && v < cur)
}

// eventAgg is what the rows need from analytics events: each batch's summary,
// which sessions each account and batch speak for, and the events rows are built
// from. A part's is kept across runs (rowParts); a run merges the parts' in
// table order.
type eventAgg struct {
	batches    map[string]*batchOf
	eventCount map[string]int64           // session -> its Claude Code events
	lastEvent  map[string]int64           // session -> its newest Claude Code event's time
	eventCorrs map[string]map[string]bool // session -> the batches that carried its Claude Code events
	byAccount  map[string]map[string]bool // account -> its sessions
	known      map[string]bool            // sessions some account appears on
	reports    map[string][]rowEvent      // session -> tengu_api_success (oldest first, once sorted)
	retries    map[string][]rowEvent      // session -> tengu_api_retry (oldest first, once sorted)
	prompts    map[string][]rowEvent      // session -> tengu_input_prompt, in table order

	// For the prompt rows (promptrows.go).
	promptEvs map[string][]*promptEvent // session -> its prompt-row events (promptEventNames)
	turns     map[string][]codexTurn    // session -> Codex's turn reports, in table order
	keys      map[string][]batchKey     // while a part is read: each batch's Claude Code events
	unnamed   map[string]bool           // while a part is read: batches with a prompt that has no id

	// For the session rows (sessionrows.go).
	sess map[string]*sessEvents

	// For the session marks (sessionmarks.go).
	markEvs map[string][]*markEvent // session -> its markEventNames events

	// For the presence segments (presence.go).
	presence map[string]*presenceEvs // session -> its blur and sleep windows

	// For the person tables (personmap.go).
	accts map[string]*acctEvents // account -> what its events say about it

	// For the event rows (eventrows.go).
	ccEvs     map[string][]*listedEvent // session -> its Claude Code events
	ccBatches map[string]bool           // the batches with a Claude Code event
	part      string                    // while a part is read: its key,
	ccPos     int                       // the place of its next Claude Code event,
	ccAll     []*listedEvent            // and its events, in table order
	in        interner                  // shares the event rows' repeated strings
}

func newEventAgg() *eventAgg {
	return &eventAgg{
		batches: map[string]*batchOf{}, eventCount: map[string]int64{}, lastEvent: map[string]int64{}, eventCorrs: map[string]map[string]bool{},
		byAccount: map[string]map[string]bool{}, known: map[string]bool{},
		reports: map[string][]rowEvent{}, retries: map[string][]rowEvent{}, prompts: map[string][]rowEvent{},
		promptEvs: map[string][]*promptEvent{}, turns: map[string][]codexTurn{},
		keys: map[string][]batchKey{}, unnamed: map[string]bool{},
		sess: map[string]*sessEvents{}, markEvs: map[string][]*markEvent{}, presence: map[string]*presenceEvs{},
		accts: map[string]*acctEvents{},
		ccEvs: map[string][]*listedEvent{}, ccBatches: map[string]bool{}, in: interner{},
	}
}

// merge folds in a later part's events, copying (a cached part is never changed).
func (a *eventAgg) merge(o *eventAgg) {
	for corr, b := range o.batches {
		cur := a.batches[corr]
		if cur == nil {
			c := *b
			a.batches[corr] = &c
			continue
		}
		cur.n += b.n
		if earlier(b.sessionNS, b.session, cur.sessionNS, cur.session) {
			cur.session, cur.sessionNS = b.session, b.sessionNS
		}
		if b.name != "" && (cur.name == "" || b.name < cur.name) {
			cur.name = b.name
		}
	}
	for s, n := range o.eventCount {
		a.eventCount[s] += n
	}
	for s, t := range o.lastEvent {
		if cur, ok := a.lastEvent[s]; !ok || t > cur {
			a.lastEvent[s] = t
		}
	}
	for _, m := range [][2]map[string]map[string]bool{{a.eventCorrs, o.eventCorrs}, {a.byAccount, o.byAccount}} {
		for k, set := range m[1] {
			for v := range set {
				addTo(m[0], k, v)
			}
		}
	}
	for s := range o.known {
		a.known[s] = true
	}
	for _, m := range [][2]map[string][]rowEvent{{a.reports, o.reports}, {a.retries, o.retries}, {a.prompts, o.prompts}} {
		for s, evs := range m[1] {
			m[0][s] = append(m[0][s], evs...)
		}
	}
	for s, evs := range o.promptEvs {
		a.promptEvs[s] = append(a.promptEvs[s], evs...)
	}
	for s, ts := range o.turns {
		a.turns[s] = append(a.turns[s], ts...)
	}
	for s, x := range o.sess {
		a.sessOf(s).merge(x)
	}
	for s, evs := range o.markEvs {
		a.markEvs[s] = append(a.markEvs[s], evs...)
	}
	for s, x := range o.presence {
		p := a.presenceOf(s)
		p.blur, p.sleep = append(p.blur, x.blur...), append(p.sleep, x.sleep...)
	}
	for s, evs := range o.ccEvs {
		a.ccEvs[s] = append(a.ccEvs[s], evs...)
	}
	for acct, x := range o.accts {
		a.acctOf(acct).merge(x)
	}
}

// rowData is everything the rows are built from, indexed.
type rowData struct {
	eventAgg
	calls       []*rowCall
	txByHash    map[string][]int            // key_hash -> its transactions
	corrHashes  map[string][]string         // correlation_id -> its calls' key_hashes
	callHashes  map[string]map[string]bool  // session -> its visible model calls' key_hashes
	reqIDs      map[string]map[string]bool  // session -> its visible model calls' request ids
	clientIDs   map[string]map[string]bool  // session -> their client request ids
	callSess    map[string]bool             // sessions with a visible model call
	codexPrompt map[string]map[string]int64 // session -> Codex turn -> its number in the thread
	accounts    []Account
}

// newRowData indexes the calls (in table order, then the in-flight ones) and the
// events parts (in table order).
func newRowData(calls []*rowCall, events []*eventAgg, accounts []Account) *rowData {
	d := &rowData{eventAgg: *newEventAgg(), calls: calls, accounts: accounts}
	for _, e := range events {
		d.merge(e)
	}
	d.sortEvents()
	d.index()
	return d
}

// partRows is what one fact part gives the request rows. Parts never change, so
// it's kept across runs, and a run reads only the parts it hasn't seen.
type partRows struct {
	calls  []*rowCall // a requests part's
	events *eventAgg  // an events part's
}

// rowParts reads the fact parts for the request rows, keeping what each gives
// across runs: parts never change, so a run decodes only the parts it hasn't seen.
// Its strings are copied out of the parts' buffers, one copy each.
type rowParts struct {
	out     Store
	cache   map[string]*partRows
	strings interner
}

func newRowParts(out Store) *rowParts {
	return &rowParts{out: out, cache: map[string]*partRows{}, strings: interner{}}
}

// Derived is the snapshot tables built from all the committed data each run
// (docs/architecture.md, Decision 4).
type Derived struct {
	RequestRows  []RequestRow
	PersonLast   []PersonLast
	PromptRows   []PromptRow
	PromptSteps  []PromptStep
	SessionRows  []SessionRow
	SessionMarks []SessionMark
	Presence     []PresenceSegment
	EventRows    []EventRow
	SessionTools []SessionTool
	SessionReqs  []SessionRequestRow
	PersonMap    []PersonMapRow
	PersonSess   []PersonSession
	AcctFacts    []AccountFact
	AcctDevices  []AccountDevice
	CallTools    []CallToolUse
}

// derive builds the derived tables from the committed data the manifest names,
// plus the in-flight transactions and the accounts and people committed with
// them.
func (rp *rowParts) derive(ctx context.Context, m *Manifest, pending []Pending, accounts []Account, people []Person, sessions []Session) (*Derived, error) {
	named := map[string]bool{}
	var calls []*rowCall
	for _, k := range m.Facts["requests"] {
		named[k] = true
		part := rp.cache[k]
		if part == nil {
			part = &partRows{}
			err := eachParquet(ctx, rp.out, k, func(r requestCols) {
				if !r.Synthetic { // the API never loads the probe's rows
					c := callFromRequest(r, rp.strings)
					part.calls = append(part.calls, &c)
				}
			})
			if err != nil {
				return nil, fmt.Errorf("derived tables: read %s: %w", k, err)
			}
			rp.cache[k] = part
		}
		calls = append(calls, part.calls...)
	}
	for _, x := range pending {
		if !x.Done && x.StartNS != 0 {
			c := callFromPending(x)
			calls = append(calls, &c)
		}
	}
	var events []*eventAgg
	for _, k := range m.Facts["events"] {
		named[k] = true
		part := rp.cache[k]
		if part == nil {
			part = &partRows{events: newEventAgg()}
			part.events.part, part.events.in = k, rp.strings
			add := func(e eventCols) {
				e.CorrelationID, e.SessionID = rp.strings.of(e.CorrelationID), rp.strings.of(e.SessionID)
				e.Name, e.PersonID = rp.strings.of(e.Name), rp.strings.of(e.PersonID)
				e.DeviceID = rp.strings.of(e.DeviceID)
				e.PromptID = rp.strings.of(e.PromptID)
				e.HeadSHA = rp.strings.of(e.HeadSHA)
				part.events.add(e)
			}
			if err := eachParquet(ctx, rp.out, k, add); err != nil {
				return nil, fmt.Errorf("derived tables: read %s: %w", k, err)
			}
			part.events.numberEvents()
			part.events.numberUnnamed()
			rp.cache[k] = part
		}
		events = append(events, part.events)
	}
	for k := range rp.cache {
		if !named[k] { // replaced by a compaction
			delete(rp.cache, k)
		}
	}
	d := newRowData(calls, events, accounts)
	if err := rp.renumberSpanning(ctx, m.Facts["events"], d); err != nil {
		return nil, err
	}
	ids := map[string]bool{unattributed: true}
	for _, a := range accounts {
		ids[a.AccountID] = true
	}
	for _, x := range people {
		ids[x.PersonID] = true
	}
	out := &Derived{}
	out.RequestRows, out.PersonLast = d.rows(sortedKeys(ids))
	out.PromptRows, out.PromptSteps, out.SessionMarks = d.promptRows()
	out.SessionRows = d.sessionRows(sessions)
	out.Presence = d.presenceSegments(out.SessionRows)
	out.EventRows, out.SessionTools = d.eventRows()
	out.SessionReqs = d.sessionRequestRows(out.SessionRows)
	pt := d.personTables(sortedKeys(ids))
	out.PersonMap, out.PersonSess, out.AcctFacts, out.AcctDevices, out.CallTools = pt.Map, pt.Sessions, pt.Facts, pt.Devices, pt.Tools
	return out, nil
}

// Derive builds the derived tables from a committed output (the manifest under
// prefix) without processing anything: `aintercept derive`, the parity test's
// quick loop over an existing shaped directory.
func Derive(ctx context.Context, s Store, prefix string) (*Derived, error) {
	b, err := s.Get(ctx, path.Join(strings.Trim(prefix, "/"), "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var pending []Pending
	var accounts []Account
	var people []Person
	var sessions []Session
	for table, into := range map[string]func(string) error{
		"sessions": func(k string) error { return readInto(ctx, s, []string{k}, &sessions) },
		"pending":  func(k string) error { return readInto(ctx, s, []string{k}, &pending) },
		"accounts": func(k string) error { return readInto(ctx, s, []string{k}, &accounts) },
		"people":   func(k string) error { return readInto(ctx, s, []string{k}, &people) },
	} {
		if k := m.Snapshots[table]; k != "" {
			if err := into(k); err != nil {
				return nil, err
			}
		}
	}
	return newRowParts(s).derive(ctx, &m, pending, accounts, people, sessions)
}

// EncodeParquet is rows as a Parquet file, as a run writes a snapshot.
func EncodeParquet[T any](rows []T) []byte { return encodeParquet(rows) }

// rows builds every person's rows, sorted by person, time and id, and each
// person's newest data time: the API's lastDataAt for the person, the latest of
// their sessions' visible model calls and Claude Code events (Session.last).
func (d *rowData) rows(people []string) ([]RequestRow, []PersonLast) {
	callLast := map[string]int64{}
	for _, c := range d.calls {
		if c.ccCall() {
			if cur, ok := callLast[c.session]; !ok || c.ts > cur {
				callLast[c.session] = c.ts
			}
		}
	}
	var out []RequestRow
	var lasts []PersonLast
	for _, person := range people {
		var sids map[string]bool
		if person == unattributed {
			sids = d.unattributedSessions()
		} else {
			sids = d.personSessions(d.linkedAccounts(person))
		}
		var last *int64
		for s := range sids {
			for _, m := range []map[string]int64{callLast, d.lastEvent} {
				if t, ok := m[s]; ok && (last == nil || t > *last) {
					last = &t
				}
			}
		}
		lasts = append(lasts, PersonLast{PersonID: person, LastNS: last})
		rows := d.personRows(sids, "")
		for i := range rows {
			rows[i].PersonID = person
		}
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			return a.TsNS < b.TsNS || a.TsNS == b.TsNS && a.RequestID < b.RequestID
		})
		out = append(out, rows...)
	}
	return out, lasts
}

func (d *rowData) index() {
	d.txByHash, d.corrHashes = map[string][]int{}, map[string][]string{}
	d.callHashes, d.reqIDs, d.clientIDs = map[string]map[string]bool{}, map[string]map[string]bool{}, map[string]map[string]bool{}
	d.callSess = map[string]bool{}
	type turn struct {
		first  int64
		prompt string
	}
	codex := map[string]map[string]int64{} // session -> turn -> its first call
	for i, c := range d.calls {
		if c.keyHash != "" {
			d.corrHashes[c.corrID] = append(d.corrHashes[c.corrID], c.keyHash)
			if c.transaction() {
				d.txByHash[c.keyHash] = append(d.txByHash[c.keyHash], i)
			}
		}
		if !c.ccCall() {
			continue
		}
		s := c.session
		d.callSess[s] = true
		if c.keyHash != "" {
			addTo(d.callHashes, s, c.keyHash)
		}
		if c.reqID != "" {
			addTo(d.reqIDs, s, c.reqID)
		}
		if c.clientReqID != "" {
			addTo(d.clientIDs, s, c.clientReqID)
		}
		if c.person != "" {
			addTo(d.byAccount, c.person, s)
			d.known[s] = true
		}
		if c.product == productCodex && c.prompt != "" {
			if codex[s] == nil {
				codex[s] = map[string]int64{}
			}
			if at, ok := codex[s][c.prompt]; !ok || c.ts < at {
				codex[s][c.prompt] = c.ts
			}
		}
	}
	// A Codex turn's number is its place in the thread, by its first call.
	d.codexPrompt = map[string]map[string]int64{}
	for s, turns := range codex {
		order := make([]turn, 0, len(turns))
		for pid, at := range turns {
			order = append(order, turn{at, pid})
		}
		sort.Slice(order, func(i, j int) bool {
			return order[i].first < order[j].first || order[i].first == order[j].first && order[i].prompt < order[j].prompt
		})
		d.codexPrompt[s] = map[string]int64{}
		for n, t := range order {
			d.codexPrompt[s][t.prompt] = int64(n + 1)
		}
	}
}

// add folds one analytics event in, in table order.
func (d *eventAgg) add(e eventCols) {
	if e.Source != productClaudeCode && e.Source != productCodex {
		return
	}
	b := d.batches[e.CorrelationID]
	if b == nil {
		b = &batchOf{}
		d.batches[e.CorrelationID] = b
	}
	b.n++
	if earlier(e.NS, e.SessionID, b.sessionNS, b.session) {
		b.session, b.sessionNS = e.SessionID, e.NS
	}
	if e.Name != "" && (b.name == "" || e.Name < b.name) {
		b.name = e.Name
	}
	if e.Source == productCodex && e.Name == "codex_turn_event" && e.SessionID != "" {
		d.sessOf(e.SessionID).addTurn(e)
	}
	if e.Source == productCodex && e.Name == "codex_turn_event" && e.SessionID != "" && e.PromptID != "" {
		d.turns[e.SessionID] = append(d.turns[e.SessionID], codexTurn{ts: e.NS, prompt: e.PromptID, report: turnReport(e.MetadataJSON)})
	}
	if e.Source != productClaudeCode {
		return
	}
	d.keys[e.CorrelationID] = append(d.keys[e.CorrelationID], batchKey{e.Seq, e.RowNS, e.NS, firstStr(e.Name, "(unnamed)"), e.MetadataJSON})
	d.ccBatches[e.CorrelationID] = true
	pos := d.ccPos
	d.ccPos++
	if e.PersonID != "" && e.SessionID != "" {
		addTo(d.byAccount, e.PersonID, e.SessionID)
		d.known[e.SessionID] = true
	}
	if e.PersonID != "" {
		d.acctOf(e.PersonID).add(e)
	}
	s := e.SessionID
	if s == "" {
		return
	}
	d.eventCount[s]++
	if cur, ok := d.lastEvent[s]; !ok || e.NS > cur {
		d.lastEvent[s] = e.NS
	}
	d.sessOf(s).add(e)
	addTo(d.eventCorrs, s, e.CorrelationID)
	ce := newListedEvent(e, d.part, pos, d.in)
	if ce.sums != nil {
		d.sessOf(s).addSums(e.NS, ce.sums)
	}
	d.ccEvs[s] = append(d.ccEvs[s], ce)
	d.ccAll = append(d.ccAll, ce)
	if promptEventNames[e.Name] {
		pe := newPromptEvent(e)
		pe.part, pe.pos = d.part, pos
		d.promptEvs[s] = append(d.promptEvs[s], pe)
		if pe.unnamedInput() {
			d.unnamed[e.CorrelationID] = true
		}
	}
	if markEventNames[e.Name] {
		me := newMarkEvent(e)
		me.part, me.pos = d.part, pos
		d.markEvs[s] = append(d.markEvs[s], me)
	}
	if presenceEventNames[e.Name] {
		d.addPresence(s, e)
	}
	switch e.Name {
	case "tengu_api_success":
		md := jsonFields(e.MetadataJSON, "requestId", "clientRequestId", "ttftMs",
			"cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens")
		rid, _ := jsonString(md["requestId"])
		crid, _ := jsonString(md["clientRequestId"])
		d.reports[s] = append(d.reports[s], rowEvent{ts: e.NS, key: strings.Clone(e.EventKey), session: s, name: e.Name, reqID: rid, clientReqID: crid,
			ttft:    jsonDouble(md["ttftMs"]),
			cache5m: jsonInt(md["cache_creation_5m_input_tokens"], math.MinInt64, math.MaxInt64),
			cache1h: jsonInt(md["cache_creation_1h_input_tokens"], math.MinInt64, math.MaxInt64)})
	case "tengu_api_retry":
		md := jsonFields(e.MetadataJSON, "cc_prompt_id", "attempt")
		pid, ok := jsonString(md["cc_prompt_id"])
		d.retries[s] = append(d.retries[s], rowEvent{ts: e.NS, key: strings.Clone(e.EventKey), session: s, name: e.Name, promptID: pid, hasPromptID: ok,
			attempt: jsonInt(md["attempt"], math.MinInt32, math.MaxInt32)})
	case "tengu_input_prompt":
		md := jsonFields(e.MetadataJSON, "cc_prompt_id", "prompt_index")
		pid, ok := jsonString(md["cc_prompt_id"])
		d.prompts[s] = append(d.prompts[s], rowEvent{ts: e.NS, key: strings.Clone(e.EventKey), session: s, name: e.Name, promptID: pid, hasPromptID: ok,
			index: jsonInt(md["prompt_index"], math.MinInt32, math.MaxInt32)})
	}
}

// sortEvents puts reports, retries and input prompts in (time, event key) order, so
// a pick among events at the same time doesn't depend on the order rows were read.
func (d *eventAgg) sortEvents() {
	for _, m := range []map[string][]rowEvent{d.reports, d.retries, d.prompts} {
		for _, evs := range m {
			sortRowEvents(evs)
		}
	}
}

func sortRowEvents(evs []rowEvent) {
	sort.Slice(evs, func(i, j int) bool {
		return evs[i].ts < evs[j].ts || evs[i].ts == evs[j].ts && evs[i].key < evs[j].key
	})
}

// linkedAccounts is the person's accounts: this one, and every account with the
// same email (nothing weaker than an email links accounts).
func (d *rowData) linkedAccounts(person string) []string {
	out := []string{person}
	email := ""
	for _, a := range d.accounts {
		if a.AccountID == person {
			email = strings.ToLower(a.Email)
		}
	}
	if email == "" {
		return out
	}
	for _, a := range d.accounts {
		if a.AccountID != person && a.Email != "" && strings.ToLower(a.Email) == email {
			out = append(out, a.AccountID)
		}
	}
	return out
}

// personSessions is every session any of the accounts appears on.
func (d *rowData) personSessions(accounts []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range accounts {
		for s := range d.byAccount[a] {
			out[s] = true
		}
	}
	return out
}

// unattributedSessions is the sessions of visible model calls no account appears on.
func (d *rowData) unattributedSessions() map[string]bool {
	out := map[string]bool{}
	for s := range d.callSess {
		if !d.known[s] {
			out[s] = true
		}
	}
	return out
}

// reported is what a call's tengu_api_success says about it.
type reported struct {
	ttft             *float64
	cache5m, cache1h *int64
}

// personRows is the rows of the transactions made with the sessions' credentials,
// plus the sessions' reported calls that weren't captured. With `only`, just that
// session's transactions (person.py transactions' session_id).
func (d *rowData) personRows(sids map[string]bool, only string) []RequestRow {
	sessions := sortedKeys(sids)
	// The prompts' numbers: Claude Code's from tengu_input_prompt (the latest by
	// time and event key, when a prompt id repeats), a Codex turn's from its place
	// in the thread (the later session wins, as the API's dict does).
	var inputs []rowEvent
	for _, s := range sessions {
		inputs = append(inputs, d.prompts[s]...)
	}
	sortRowEvents(inputs)
	index := map[string]int64{}
	for _, e := range inputs {
		if e.promptID != "" && e.index != nil {
			index[e.promptID] = *e.index
		}
	}
	for _, s := range sessions {
		for pid, n := range d.codexPrompt[s] {
			index[pid] = n
		}
	}
	// Each request id's first report, by time.
	var reps []rowEvent
	for _, s := range sessions {
		reps = append(reps, d.reports[s]...)
	}
	sortRowEvents(reps)
	reports := map[string]reported{}
	for _, e := range reps {
		for _, key := range []string{e.reqID, e.clientReqID} {
			if _, ok := reports[key]; key != "" && !ok {
				reports[key] = reported{e.ttft, e.cache5m, e.cache1h}
			}
		}
	}
	// The credentials: the sessions' model calls', and their analytics batches'.
	hashes := map[string]bool{}
	for _, s := range sessions {
		for h := range d.callHashes[s] {
			hashes[h] = true
		}
		for corr := range d.eventCorrs[s] {
			for _, h := range d.corrHashes[corr] {
				hashes[h] = true
			}
		}
	}
	var txs []*rowCall
	for h := range hashes {
		for _, i := range d.txByHash[h] {
			if only == "" || d.txSession(d.calls[i]) == only {
				txs = append(txs, d.calls[i])
			}
		}
	}
	sort.Slice(txs, func(i, j int) bool {
		return txs[i].ts < txs[j].ts || txs[i].ts == txs[j].ts && txs[i].id < txs[j].id
	})
	tried := d.attempts(txs)
	steps := stepIndexes(txs)
	rows := make([]RequestRow, 0, len(txs))
	for _, t := range txs {
		rows = append(rows, d.requestRow(t, index, reports, tried[t.id], steps))
	}
	for _, s := range sessions {
		rows = append(rows, d.missedRows(s, reports)...)
	}
	return rows
}

// SessionRequestRow is a session_request_rows row: a row of a session's Requests
// tab (session.py request_rows), under the session it's listed for (scope), seq
// its place there by time and id. The row is a group of request_rows' columns
// (embedded, parquet-go writes an optional 0 as null), its person_id "": a
// session's rows are built over that session alone.
type SessionRequestRow struct {
	Scope string     `parquet:"scope_session_id"`
	Seq   int64      `parquet:"seq"`
	Row   RequestRow `parquet:"row"`
}

// sessionRequestRows is each session's Requests tab: the request log's rows built
// over the session alone, its own transactions and not-captured calls. A Codex
// thread also gets its client's calls no thread id names (usage, catalog, metrics,
// analytics: handoff §11L.6), by time: the same client of the same credentials,
// from the thread's start to a minute after its last activity (shown times, in µs).
func (d *rowData) sessionRequestRows(sessions []SessionRow) []SessionRequestRow {
	var out []SessionRequestRow
	for _, r := range sessions {
		s := r.SessionID
		sids := map[string]bool{s: true}
		codex := r.Product == productCodex
		only := s
		if codex {
			only = ""
		}
		rows := d.personRows(sids, only)
		lo, hi := r.StartNS/1000, (r.LastNS+60*1_000_000_000)/1000
		client := deref(r.Client)
		var kept []RequestRow
		for _, x := range rows {
			if deref(x.SessionID) == s && x.SessionID != nil ||
				codex && x.SessionID == nil && deref(x.Agent) == "codex" && x.Kind != "model" &&
					deref(x.Client) == client && lo <= x.TsNS/1000 && x.TsNS/1000 <= hi {
				kept = append(kept, x)
			}
		}
		sort.SliceStable(kept, func(i, j int) bool {
			a, b := kept[i], kept[j]
			return a.TsNS < b.TsNS || a.TsNS == b.TsNS && a.RequestID < b.RequestID
		})
		for i, x := range kept {
			out = append(out, SessionRequestRow{Scope: s, Seq: int64(i), Row: x})
		}
	}
	return out
}

// attempt is a failed call Claude Code retried: its tengu_api_retry and the
// next attempt, when captured.
type attempt struct {
	retry rowEvent
	next  *rowCall
}

// failed: no status though it ended, or a 4xx/5xx.
func (c *rowCall) failed() bool { return c.status == 0 && c.end != 0 || c.status >= 400 }

// txSession is a transaction's session: its own, else its analytics batch's.
func (d *rowData) txSession(c *rowCall) string {
	if c.session != "" {
		return c.session
	}
	if b := d.batches[c.id]; b != nil {
		return b.session
	}
	return ""
}

// attempts maps each failed model call Claude Code retried to its retry. The
// next attempt is the next model call (in time order) of the same session,
// prompt and request class.
func (d *rowData) attempts(txs []*rowCall) map[string]*attempt {
	out := map[string]*attempt{}
	for i, t := range txs {
		s := d.txSession(t)
		if kindOf(t.path) != "model" || !t.failed() || s == "" {
			continue
		}
		r := nearestRetry(d.retries[s], t.ts, t.end, t.prompt)
		if r == nil {
			continue
		}
		a := &attempt{retry: *r}
		for _, x := range txs[i+1:] {
			if kindOf(x.path) == "model" && d.txSession(x) == s && x.prompt == t.prompt && x.class == t.class {
				a.next = x
				break
			}
		}
		out[t.id] = a
	}
	return out
}

// nearestRetry is the tengu_api_retry for a failed attempt. It carries no request
// id, so: same prompt (when both have one), logged between the attempt's start
// and 30 s after its end; the nearest to its end, the earliest of equals (retries
// come in time and event-key order, so equal times go to the least key).
func nearestRetry(retries []rowEvent, ts, end int64, prompt string) *rowEvent {
	stop := end
	if stop == 0 {
		stop = ts
	}
	var best *rowEvent
	var bestDist int64
	for i := range retries {
		r := &retries[i]
		if r.ts < ts || r.ts > stop+retryAfterNS {
			continue
		}
		if prompt != "" && r.hasPromptID && r.promptID != prompt {
			continue
		}
		dist := r.ts - stop
		if dist < 0 {
			dist = -dist
		}
		if best == nil || dist < bestDist || dist == bestDist && r.ts < best.ts {
			best, bestDist = r, dist
		}
	}
	return best
}

// stepIndexes is each visible model call's step number within its prompt
// (1-based, in time order).
func stepIndexes(txs []*rowCall) map[string]int64 {
	sorted := append([]*rowCall(nil), txs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ts < sorted[j].ts || sorted[i].ts == sorted[j].ts && sorted[i].id < sorted[j].id
	})
	out, seen := map[string]int64{}, map[string]int64{}
	for _, t := range sorted {
		if kindOf(t.path) == "model" && t.prompt != "" && t.hidden == "" {
			seen[t.prompt]++
			out[t.id] = seen[t.prompt]
		}
	}
	return out
}

var backgroundClasses = map[string]bool{"auxiliary": true, "compaction": true}

// requestRow is a captured transaction's row.
func (d *rowData) requestRow(t *rowCall, index map[string]int64, reports map[string]reported, a *attempt, steps map[string]int64) RequestRow {
	kind := kindOf(t.path)
	model := kind == "model"
	var rep *reported
	if model {
		if r, ok := reports[t.reqID]; ok && t.reqID != "" {
			rep = &r
		} else if r, ok := reports[t.clientReqID]; ok && t.clientReqID != "" {
			rep = &r
		}
	}
	row := RequestRow{
		RequestID: t.id, TsNS: t.ts, Kind: kind,
		Agent:           ptr(agentOf(t.product)),
		Client:          nz(t.client),
		SessionID:       nz(d.txSession(t)),
		Method:          nz(t.method),
		Path:            t.path,
		Detail:          d.detail(t, kind),
		BytesUp:         nzi(t.up),
		BytesDown:       nzi(t.down),
		Background:      kind == "count" || kind == "analytics" || backgroundClasses[t.class],
		Captured:        true,
		ContentEncoding: nz(t.encoding),
		ToolCalls:       []string{},
	}
	if t.product == "" {
		row.Agent = nil
	}
	if t.hidden == "prewarm" || t.hidden == "title" {
		row.HiddenKind = ptr(t.hidden)
	}
	if t.prompt != "" {
		if n, ok := index[t.prompt]; ok {
			row.PromptIndex = &n
		}
	}
	switch {
	case t.status != 0:
		row.Status = ptr(t.status)
	case t.end != 0:
		row.StatusErr = true
	}
	if t.end != 0 {
		row.DurationMS = ptr(round1(float64(t.end-t.ts) / 1e6))
	}
	if a != nil {
		row.Retries = a.retry.attempt
	}
	if t.status != 0 {
		row.StatusText = statusPhrase(t.status)
	} else {
		row.StatusText = nz(t.errText)
	}
	if rep != nil {
		row.TTFTMS = rep.ttft
	}
	if model {
		row.StopReason, row.Model, row.Effort = nz(t.stop), nz(t.model), nz(t.effort)
		row.TokensInput, row.TokensCacheRead, row.TokensCacheWrite = nzi(t.input), nzi(t.cacheRead), nzi(t.cacheWrite)
		if rep != nil {
			row.TokensCacheWrite5m, row.TokensCacheWrite1h = rep.cache5m, rep.cache1h
		}
		row.TokensOutput = nzi(t.output)
		prewarm := t.hidden == "prewarm"
		if t.output == 0 && prewarm { // a prewarm produces nothing: its output is 0, not unknown
			row.TokensOutput = ptr(int64(0))
		}
		row.TokensAccuracy = ptr("missing")
		if t.output != 0 || prewarm {
			row.TokensAccuracy = ptr("exact")
		}
		row.ToolCalls = append(row.ToolCalls, t.tools...)
		row.UsageMissing = usageMissing(t)
		row.Text = t.text
	}
	if t.failed() {
		msg := t.errText
		if msg == "" && t.status != 0 {
			if ph := statusPhrase(t.status); ph != nil {
				msg = *ph
			}
		}
		if msg == "" {
			msg = "HTTP " + pyInt(t.status)
		}
		row.ErrorMessage = &msg
		row.ErrorAfterMS = row.DurationMS
		row.ErrorBytesReceived = nzi(t.down)
		if a != nil {
			row.ErrorRetries = a.retry.attempt
			if n := a.next; n != nil && n.status != 0 && n.status < 400 {
				row.ErrorRetrySucceededNS = ptr(n.ts)
			}
		}
	}
	if s := steps[t.id]; s != 0 {
		row.StepIndex = &s
	}
	return row
}

// missedRows is a session's calls Claude Code reported that the proxy never saw
// (only what each tengu_api_success carries); none when it has no analytics.
func (d *rowData) missedRows(s string, reports map[string]reported) []RequestRow {
	if d.eventCount[s] == 0 {
		return nil
	}
	var out []RequestRow
	for _, e := range d.reports[s] {
		if d.reqIDs[s][e.reqID] || d.clientIDs[s][e.clientReqID] {
			continue
		}
		var rep *reported
		if r, ok := reports[e.reqID]; ok && e.reqID != "" {
			rep = &r
		} else if r, ok := reports[e.clientReqID]; ok && e.clientReqID != "" {
			rep = &r
		}
		id := firstStr(e.reqID, e.clientReqID)
		if id == "" {
			id = strconv.FormatInt(e.ts, 10)
		}
		row := RequestRow{
			RequestID: "reported:" + id, TsNS: e.ts, Kind: "model", Agent: ptr("claude-code"),
			SessionID: ptr(s), Path: "/v1/messages", Detail: "reported by Claude Code · not captured",
			TokensAccuracy: ptr("missing"), ToolCalls: []string{},
		}
		if rep != nil {
			row.TTFTMS, row.TokensCacheWrite5m, row.TokensCacheWrite1h = rep.ttft, rep.cache5m, rep.cache1h
		}
		out = append(out, row)
	}
	return out
}

// usageMissing is why a model call has no usage; nil when it has it.
func usageMissing(t *rowCall) *string {
	switch {
	case t.output != 0 || t.hidden == "prewarm": // a prewarm's output is 0, not missing
		return nil
	case strings.Contains(t.stop, "interrupt"):
		return ptr("interrupted")
	case t.status >= 400:
		return ptr("failed")
	case t.wsTurn == 0 && t.down > parseBuffer:
		return ptr("parseBuffer")
	}
	return ptr("streamCut")
}

// kindOf is a request's channel, by its path (Codex's: model, analytics, metrics,
// usage, catalog; Claude Code's: model, count, analytics, flags, account).
func kindOf(path string) string {
	p, _, _ := strings.Cut(path, "?")
	switch {
	case p == "/backend-api/codex/responses":
		return "model"
	case strings.HasPrefix(p, "/backend-api/codex/analytics-events"):
		return "analytics"
	case strings.HasPrefix(p, "/otlp/"):
		return "metrics"
	case strings.HasPrefix(p, "/backend-api/wham/"), strings.HasPrefix(p, "/backend-api/accounts/"):
		return "usage"
	case strings.HasPrefix(p, "/backend-api/ps/"), strings.HasPrefix(p, "/backend-api/plugins/"),
		strings.HasPrefix(p, "/backend-api/codex/models"):
		return "catalog"
	case strings.HasPrefix(p, "/v1/messages/count_tokens"):
		return "count"
	case strings.HasPrefix(p, "/v1/messages"):
		return "model"
	case strings.HasPrefix(p, "/api/event_logging"):
		return "analytics"
	case strings.HasPrefix(p, "/api/eval/"):
		return "flags"
	}
	return "account"
}

var modelFamilyRe = regexp.MustCompile(`^claude-(opus|sonnet|haiku)`)

func modelFamily(model string) string {
	if model == "" {
		return "—"
	}
	if m := modelFamilyRe.FindStringSubmatch(model); m != nil {
		return m[1]
	}
	return model
}

// detail is the row's one-line summary.
func (d *rowData) detail(t *rowCall, kind string) string {
	switch kind {
	case "model":
		return firstStr(t.class, "—") + " · " + modelFamily(t.model) + " · " + firstStr(t.stop, "—")
	case "analytics":
		var n int64
		name := ""
		if b := d.batches[t.id]; b != nil {
			n, name = b.n, b.name
		}
		if t.product == productCodex && name != "" { // its event, by name
			if n == 1 {
				return name
			}
			return name + " · " + strconv.FormatInt(n, 10) + " events"
		}
		return strconv.FormatInt(n, 10) + " events"
	case "flags":
		return "feature flags"
	case "count":
		return "count_tokens"
	}
	p, _, _ := strings.Cut(t.path, "?")
	if kind == "usage" || kind == "catalog" || kind == "metrics" { // Codex's endpoints, by name: wham/usage
		return firstStr(strings.TrimLeft(strings.TrimPrefix(p, "/backend-api/"), "/"), p)
	}
	return firstStr(p[strings.LastIndex(p, "/")+1:], t.path)
}

// agentOf is the UI's agent for a product (rows from before products were tagged
// are Claude Code's).
func agentOf(product string) string {
	if product == productCodex {
		return "codex"
	}
	return "claude-code"
}

// toolLabels is a call's tool calls as labels, "exec ×2", in order.
func toolLabels(toolUsesJSON string) []string {
	if toolUsesJSON == "" {
		return nil
	}
	var uses []any
	if json.Unmarshal([]byte(toolUsesJSON), &uses) != nil {
		return nil
	}
	var out []string
	for _, u := range uses {
		m, ok := u.(map[string]any)
		if !ok {
			continue
		}
		name := "?"
		if s, ok := m["name"].(string); ok && s != "" {
			name = s
		} else if s, ok := m["tool"].(string); ok && s != "" {
			name = s
		}
		n := int64(1)
		if c, ok := m["count"].(float64); ok && c != 0 {
			n = int64(c)
		}
		out = append(out, name+" ×"+strconv.FormatInt(n, 10))
	}
	return out
}

// round1 rounds to one decimal place as Python's round(x, 1) does: the decimal
// nearest the double's exact value, an exact tie to even (0.25 -> 0.2).
func round1(x float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	return f
}

// statusPhrase is a status code's reason phrase as Python's http.HTTPStatus has
// it (3.13); nil for a code it doesn't register (Anthropic's 529).
func statusPhrase(status int64) *string {
	if s, ok := httpPhrases[status]; ok {
		return &s
	}
	return nil
}

var httpPhrases = map[int64]string{
	100: "Continue", 101: "Switching Protocols", 102: "Processing", 103: "Early Hints",
	200: "OK", 201: "Created", 202: "Accepted", 203: "Non-Authoritative Information", 204: "No Content",
	205: "Reset Content", 206: "Partial Content", 207: "Multi-Status", 208: "Already Reported", 226: "IM Used",
	300: "Multiple Choices", 301: "Moved Permanently", 302: "Found", 303: "See Other", 304: "Not Modified",
	305: "Use Proxy", 307: "Temporary Redirect", 308: "Permanent Redirect",
	400: "Bad Request", 401: "Unauthorized", 402: "Payment Required", 403: "Forbidden", 404: "Not Found",
	405: "Method Not Allowed", 406: "Not Acceptable", 407: "Proxy Authentication Required", 408: "Request Timeout",
	409: "Conflict", 410: "Gone", 411: "Length Required", 412: "Precondition Failed", 413: "Content Too Large",
	414: "URI Too Long", 415: "Unsupported Media Type", 416: "Range Not Satisfiable", 417: "Expectation Failed",
	418: "I'm a Teapot", 421: "Misdirected Request", 422: "Unprocessable Content", 423: "Locked",
	424: "Failed Dependency", 425: "Too Early", 426: "Upgrade Required", 428: "Precondition Required",
	429: "Too Many Requests", 431: "Request Header Fields Too Large", 451: "Unavailable For Legal Reasons",
	500: "Internal Server Error", 501: "Not Implemented", 502: "Bad Gateway", 503: "Service Unavailable",
	504: "Gateway Timeout", 505: "HTTP Version Not Supported", 506: "Variant Also Negotiates",
	507: "Insufficient Storage", 508: "Loop Detected", 510: "Not Extended", 511: "Network Authentication Required",
}

// pyInt is a nullable int as Python's str() writes it (0 is null here).
func pyInt(n int64) string {
	if n == 0 {
		return "None"
	}
	return strconv.FormatInt(n, 10)
}

// --- JSON values as DuckDB reads them -------------------------------------------------

// jsonFields is the named top-level fields of a JSON object, the first of a
// repeated key (as DuckDB's json_extract_string reads them).
func jsonFields(s string, keys ...string) map[string]json.RawMessage {
	if s == "" {
		return map[string]json.RawMessage{}
	}
	// One record's metadata is read by several tables in turn: parse it once.
	// While an Apply holds the record's JSON (HoldFields), every table reads
	// one parse of it, whatever runs on other goroutines; otherwise the most
	// recent parse is reused (the single-pass reference). The map holds every
	// top-level field (the first of a repeated key); callers never change it.
	held.RLock()
	h := held.m[s]
	held.RUnlock()
	if h != nil {
		h.once.Do(func() { h.m = allFields(s) })
		return h.m
	}
	if c := lastFields.Load(); c != nil && c.s == s {
		return c.m
	}
	m := allFields(s)
	lastFields.Store(&parsedFields{s: s, m: m})
	return m
}

type parsedFields struct {
	s string
	m map[string]json.RawMessage
}

// lastFields is the most recently parsed object (shared by goroutines; each
// entry is immutable once stored).
var lastFields atomic.Pointer[parsedFields]

// held is the JSON objects Applies in progress are reading, each parsed once
// on first read and dropped when the last holder lets go.
var held = struct {
	sync.RWMutex
	m map[string]*heldFields
}{m: map[string]*heldFields{}}

type heldFields struct {
	refs int
	once sync.Once
	m    map[string]json.RawMessage
}

// HoldFields keeps the given JSON objects' parses until release is called: a
// record's tables then parse each once, even with other keys applying on
// other goroutines at the same time.
func HoldFields(ss ...string) (release func()) {
	held.Lock()
	for _, s := range ss {
		if s == "" {
			continue
		}
		h := held.m[s]
		if h == nil {
			h = &heldFields{}
			held.m[s] = h
		}
		h.refs++
	}
	held.Unlock()
	return func() {
		held.Lock()
		for _, s := range ss {
			if h := held.m[s]; h != nil {
				if h.refs--; h.refs == 0 {
					delete(held.m, s)
				}
			}
		}
		held.Unlock()
	}
}

// fieldParses counts the JSON objects parsed (FieldParses).
var fieldParses atomic.Int64

// FieldParses is how many JSON objects jsonFields has parsed.
func FieldParses() int64 { return fieldParses.Load() }

func allFields(s string) map[string]json.RawMessage {
	fieldParses.Add(1)
	out := map[string]json.RawMessage{}
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return out
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return out
		}
		k, _ := tok.(string)
		if _, seen := out[k]; !seen {
			out[k] = v
		}
	}
	return out
}

// jsonString is json_extract_string: a string's value, a scalar's text, an object's
// or array's JSON; false for null or absent.
func jsonString(v json.RawMessage) (string, bool) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || string(v) == "null" {
		return "", false
	}
	switch v[0] {
	case '"':
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s, true
		}
		return "", false
	case '{', '[':
		var b bytes.Buffer
		if json.Compact(&b, v) == nil {
			return b.String(), true
		}
	}
	return string(v), true
}

// jsonScalar is a scalar's json_extract_string, trimmed as a cast trims it;
// false for null, absent, an object or an array (no cast reads those).
func jsonScalar(v json.RawMessage) (string, bool) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || v[0] == '{' || v[0] == '[' {
		return "", false
	}
	s, ok := jsonString(v)
	return strings.TrimSpace(s), ok
}

// jsonInt is TRY_CAST(json_extract_string(…) AS INT/BIGINT): an integer in
// [lo, hi], a decimal rounded half away from zero; nil for anything else.
func jsonInt(v json.RawMessage, lo, hi int64) *int64 {
	s, ok := jsonScalar(v)
	if !ok {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < lo || n > hi {
			return nil
		}
		return &n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	f = math.Round(f)
	if f < float64(lo) || f > float64(hi) {
		return nil
	}
	n := int64(f)
	return &n
}

// jsonDouble is TRY_CAST(json_extract_string(…) AS DOUBLE).
func jsonDouble(v json.RawMessage) *float64 {
	s, ok := jsonScalar(v)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

// --- small helpers -------------------------------------------------------------------

// interner shares one copy of each repeated string (hosts, paths, sessions, models…).
type interner map[string]string

func (in interner) of(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := in[s]; ok {
		return v
	}
	v := strings.Clone(s)
	in[v] = v
	return v
}

func ptr[T any](v T) *T { return &v }

// nz is a nullable string column's value: nil for "" (null in the tables).
func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nzi is a nullable number column's value: nil for 0 (null in the tables).
func nzi(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func addTo(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
