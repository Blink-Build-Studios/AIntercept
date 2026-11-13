package state

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// The query side: today's tables rebuilt from the stored rows, as the API's SQL
// does (api/src/aintercept_api/sql/, stream_sql.py, state_sql.py). They're
// whole-session functions over rows: one key's (KeyState.Rows), from which the
// session views are rendered (sessionview.go, docs/PIPELINE.md §3.2), or several
// keys' folded together (the tests'). Each is held to the single-pass
// reference (process.DeriveRows).

// Rows is stored rows by table, then id.
type Rows = map[string]map[string]any

// tableRows is a table's rows as their type, in no order.
func tableRows[T any](rows Rows, table string) []T {
	out := make([]T, 0, len(rows[table]))
	for _, r := range rows[table] {
		out = append(out, r.(T))
	}
	return out
}

// PresenceOf is the stored presence rows in stored order: session, group, start.
func PresenceOf(rows Rows) []Presence {
	out := tableRows[Presence](rows, TablePresence)
	sortPresence(out)
	return out
}

// PresenceSegmentsOf is presence_segments as the API loads it: each session's
// rows numbered by (group, start) from 0.
func PresenceSegmentsOf(rows Rows) []process.PresenceSegment {
	ps := PresenceOf(rows)
	out := make([]process.PresenceSegment, len(ps))
	n, last := int64(0), ""
	for i, p := range ps {
		if i == 0 || p.SessionID != last {
			n, last = 0, p.SessionID
		}
		out[i] = process.PresenceSegment{SessionID: p.SessionID, Seq: n, FromNS: p.FromNS, ToNS: p.ToNS, State: p.State}
		n++
	}
	return out
}

// The event rows and session tools, rebuilt the way the query does: the stored
// rows numbered by (time, batch, number), joined to the request links, prompt
// numbers and Codex turns (the SQL in the API does the same).
func EventRowsOf(rows Rows) []process.EventRow {
	bySess := map[string][]Event{}
	for _, r := range rows[TableEvents] {
		e := r.(Event)
		bySess[e.SessionID] = append(bySess[e.SessionID], e)
	}
	links := map[[2]string]RequestLink{}
	for _, r := range rows[TableRequestLinks] {
		l := r.(RequestLink)
		links[[2]string{l.SessionID, l.RequestID}] = l
	}
	index := map[string]map[string]int64{}
	for _, r := range rows[TablePromptIndex] {
		p := r.(PromptIndex)
		if index[p.SessionID] == nil {
			index[p.SessionID] = map[string]int64{}
		}
		index[p.SessionID][p.PromptID] = p.Index
	}
	turns := map[string][]CodexTurn{}
	for _, r := range rows[TableCodexTurns] {
		c := r.(CodexTurn)
		turns[c.SessionID] = append(turns[c.SessionID], c)
	}
	for s, ts := range turns { // dense_rank over (first call, prompt): the Codex number wins
		sort.Slice(ts, func(i, j int) bool {
			return ts[i].FirstNS < ts[j].FirstNS || ts[i].FirstNS == ts[j].FirstNS && ts[i].PromptID < ts[j].PromptID
		})
		if index[s] == nil {
			index[s] = map[string]int64{}
		}
		for n, c := range ts {
			index[s][c.PromptID] = int64(n + 1)
		}
	}
	var out []process.EventRow
	var sids []string
	for s := range bySess {
		sids = append(sids, s)
	}
	sort.Strings(sids)
	for _, s := range sids {
		evs := bySess[s]
		sort.Slice(evs, func(i, j int) bool {
			a, b := evs[i], evs[j]
			switch {
			case a.TsNS != b.TsNS:
				return a.TsNS < b.TsNS
			case a.BatchID != b.BatchID:
				return a.BatchID < b.BatchID
			case a.EvSeq != b.EvSeq:
				return a.EvSeq < b.EvSeq
			case a.RowNS != b.RowNS:
				return a.RowNS < b.RowNS
			case a.Name != b.Name:
				return a.Name < b.Name
			case a.OrderMD != b.OrderMD:
				return a.OrderMD < b.OrderMD
			case a.N != b.N: // identical events: the rank ingest gave them
				return a.N < b.N
			}
			return a.EventKey < b.EventKey
		})
		for i, e := range evs {
			r := e.EventRow
			r.Seq = int64(i)
			if n, ok := index[s][e.PromptID]; ok && e.PromptID != "" {
				r.PromptIndex = &n
			}
			switch r.LinkKind {
			case "request":
				if l, ok := links[[2]string{s, r.LinkID}]; ok {
					r.LinkID, r.LinkLabel = l.CallID, l.Label
				} else {
					r.LinkID, r.LinkLabel = "reported:"+r.LinkID, "not captured"
				}
			case "prompt":
				if n, ok := index[s][r.LinkID]; ok {
					r.LinkLabel = "#" + strconv.FormatInt(n, 10)
				} else {
					r.LinkLabel = "prompt"
				}
			}
			out = append(out, r)
		}
	}
	return out
}

func SessionToolsOf(rows Rows) []process.SessionTool {
	var out []process.SessionTool
	for _, r := range rows[TableSessionTools] {
		out = append(out, r.(process.SessionTool))
	}
	sortTools(out)
	return out
}

func sortTools(out []process.SessionTool) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
}

// The query's side, as the API's SQL does it (api/src/aintercept_api/sql/prompts.sql):
// the stored rows numbered and joined back into today's prompt rows, steps and
// marks.

func lessOrd(aTS int64, aType string, aG int32, aCorr string, aSeq, aRow int64, aName, aMD, aKey string,
	bTS int64, bType string, bG int32, bCorr string, bSeq, bRow int64, bName, bMD, bKey string) bool {
	switch {
	case aTS != bTS:
		return aTS < bTS
	case aType != bType:
		return aType < bType
	case aG != bG:
		return aG < bG
	case aCorr != bCorr:
		return aCorr < bCorr
	case aSeq != bSeq:
		return aSeq < bSeq
	case aRow != bRow:
		return aRow < bRow
	case aName != bName:
		return aName < bName
	case aMD != bMD:
		return aMD < bMD
	}
	return aKey < bKey
}

// promptViewOf is every session's prompt rows, steps and marks rebuilt from the
// stored rows.
func PromptViewOf(rows Rows) ([]process.PromptRow, []process.PromptStep, []process.SessionMark) {
	sess := map[string]PromptSession{}
	for _, r := range rows[TablePromptSession] {
		p := r.(PromptSession)
		sess[p.SessionID] = p
	}
	items := map[string][]*PromptItem{}
	for _, r := range rows[TablePromptItems] {
		p := r.(PromptItem)
		items[p.Row.SessionID] = append(items[p.Row.SessionID], &p)
	}
	steps := map[string][]*StepRow{}
	for _, r := range rows[TablePromptSteps] {
		s := r.(StepRow)
		steps[s.Step.SessionID] = append(steps[s.Step.SessionID], &s)
	}
	tools := map[[2]string]process.StepTools{}
	for _, r := range rows[TablePromptStepTools] {
		t := r.(StepToolsRow)
		tools[[2]string{t.SessionID, t.RequestID}] = t.Tools
	}
	marks := map[string][]*MarkRow{}
	for _, r := range rows[TableMarks] {
		m := r.(MarkRow)
		marks[m.Mark.SessionID] = append(marks[m.Mark.SessionID], &m)
	}
	index := map[[2]string]int64{}
	for _, r := range rows[TableMarkPromptIndex] {
		p := r.(MarkPromptIndex)
		index[[2]string{p.SessionID, p.PromptID}] = p.Index
	}
	ids := map[string]bool{}
	for s := range items {
		ids[s] = true
	}
	for s := range marks {
		ids[s] = true
	}
	var sids []string
	for s := range ids {
		sids = append(sids, s)
	}
	sort.Strings(sids)

	var prows []process.PromptRow
	var stepRows []process.PromptStep
	var markRows []process.SessionMark
	for _, sid := range sids {
		ps := sess[sid]
		codex := ps.Product == canon.ProductCodex
		its := items[sid]
		sort.Slice(its, func(i, j int) bool {
			a, b := its[i], its[j]
			return lessOrd(a.Row.TNS, a.Row.Type, a.Group, a.OrdCorr, a.OrdSeq, a.OrdRowNS, a.OrdName, a.OrdMD, a.OrdKey,
				b.Row.TNS, b.Row.Type, b.Group, b.OrdCorr, b.OrdSeq, b.OrdRowNS, b.OrdName, b.OrdMD, b.OrdKey)
		})
		// The prompts in their own order (start, id): Codex's numbers and the open tail.
		var prompts []*PromptItem
		for _, it := range its {
			if it.Row.Type == "prompt" {
				prompts = append(prompts, it)
			}
		}
		sort.Slice(prompts, func(i, j int) bool {
			return prompts[i].Row.TNS < prompts[j].Row.TNS || prompts[i].Row.TNS == prompts[j].Row.TNS && prompts[i].OrdKey < prompts[j].OrdKey
		})
		rank := map[*PromptItem]int{}
		for n, p := range prompts {
			rank[p] = n
		}
		seqOf := map[string]int64{}
		for i, it := range its {
			r := it.Row
			r.Seq, r.SessionLastNS = int64(i+1), ps.LastNS
			if it.Row.Type == "prompt" {
				seqOf[*r.PromptID] = r.Seq
				r.Typed = it.HasInput || codex
				if !it.HasInput {
					r.PromptIndex = nil
					if codex {
						n := int64(rank[it] + 1)
						r.PromptIndex = &n
					}
				}
				r.OpenTail = rank[it] == len(prompts)-1 && !it.HasTurnEnd && !(codex && it.ReportStatus)
				if !codex {
					r.DurationReportMS, r.TurnReportJSON = nil, nil
				}
			}
			prows = append(prows, r)
		}
		sts := steps[sid]
		sort.Slice(sts, func(i, j int) bool {
			a, b := sts[i], sts[j]
			if seqOf[a.PromptID] != seqOf[b.PromptID] {
				return seqOf[a.PromptID] < seqOf[b.PromptID]
			}
			return a.Step.TNS < b.Step.TNS || a.Step.TNS == b.Step.TNS && a.Step.RequestID < b.Step.RequestID
		})
		n, last := int64(0), ""
		for _, s := range sts {
			if s.PromptID != last {
				n, last = 0, s.PromptID
			}
			n++
			st := s.Step
			st.Seq, st.Step = seqOf[s.PromptID], n
			st.EventsPendingIfLive = !s.HasSuccess && (!ps.HasPromptEvent || ps.LastPromptEventNS <= s.PendEnd)
			// Its request id's tools; its commands and exit codes too, unless its
			// recorded bodies name its tool calls.
			if t, ok := tools[[2]string{sid, s.ToolsRID}]; ok && s.ToolsRID != "" {
				st.ToolsJSON, st.ToolsLiveJSON = t.ToolsJSON, t.ToolsLiveJSON
				if !s.DetailCalls {
					st.Commands, st.ExitCodes = t.Commands, t.ExitCodes
					st.ContentLive, st.CommandsLive, st.ExitCodesLive = t.ContentLive, t.CommandsLive, t.ExitCodesLive
				}
			}
			stepRows = append(stepRows, st)
		}

		ms := marks[sid]
		sort.Slice(ms, func(i, j int) bool {
			a, b := ms[i], ms[j]
			if a.Group != b.Group {
				return a.Group < b.Group
			}
			if a.Mark.TNS != b.Mark.TNS {
				return a.Mark.TNS < b.Mark.TNS
			}
			if lessOrd(0, "", 0, a.OrdCorr, a.OrdSeq, a.OrdRowNS, a.OrdName, a.OrdMD, a.OrdKey,
				0, "", 0, b.OrdCorr, b.OrdSeq, b.OrdRowNS, b.OrdName, b.OrdMD, b.OrdKey) {
				return true
			}
			if lessOrd(0, "", 0, b.OrdCorr, b.OrdSeq, b.OrdRowNS, b.OrdName, b.OrdMD, b.OrdKey,
				0, "", 0, a.OrdCorr, a.OrdSeq, a.OrdRowNS, a.OrdName, a.OrdMD, a.OrdKey) {
				return false
			}
			return a.Sub < b.Sub
		})
		var out []process.SessionMark
		add := func(m process.SessionMark) {
			m.SessionID, m.Seq = sid, int64(len(out))
			out = append(out, m)
		}
		var calls []*MarkRow
		var seg *process.SessionMark
		flush := func() {
			if seg != nil {
				add(*seg)
			}
		}
		for _, m := range ms {
			x := m.Mark
			if m.PromptRef != "" {
				if n, ok := index[[2]string{sid, m.PromptRef}]; ok {
					x.PromptIndex = &n
				}
			}
			switch m.Group {
			case 0:
				calls = append(calls, m)
				add(x)
			case 4:
				if seg != nil && *seg.Mode == *x.Mode {
					continue
				}
				var prev *string
				if seg != nil {
					ts := x.TNS
					seg.ToNS, prev = &ts, seg.Mode
					flush()
				}
				mode := *x.Mode
				seg = &process.SessionMark{Kind: "permission", TNS: x.TNS, Mode: &mode, PrevMode: prev}
			default:
				add(x)
			}
		}
		flush()
		seg = nil
		for _, c := range calls {
			if c.PointIP == "" || seg != nil && *seg.IP == c.PointIP {
				continue
			}
			if seg != nil {
				ts := c.Mark.TNS
				seg.ToNS = &ts
				flush()
			}
			ip := c.PointIP
			v6 := strings.Contains(ip, ":") && !strings.HasPrefix(strings.ToLower(ip), "::ffff:")
			seg = &process.SessionMark{Kind: "network", TNS: c.Mark.TNS, IP: &ip, IPv6: v6}
		}
		flush()
		markRows = append(markRows, out...)
	}
	return prows, stepRows, markRows
}

// SessionRowsOf is session_rows rebuilt from the stored rows, as the API's SQL
// does (api/src/aintercept_api/sql/session_rows.sql), with each session's own
// label: its own title or none. A Codex thread a title thread names gets its
// label from that thread (TitledSessionRows: across keys, the merge's).
func SessionRowsOf(rows Rows) []process.SessionRow {
	out := sessionRowsUntitled(rows)
	labelSessionRows(out)
	return out
}

// TitledSessionRows is SessionRowsOf with every Codex label the rows' title
// threads give: today's session_rows over every key's rows.
func TitledSessionRows(rows Rows) []process.SessionRow {
	out := sessionRowsUntitled(rows)
	applyTitles(rows, out)
	labelSessionRows(out)
	return out
}

// labelSessionRows sets each row's labels from its titles, and clears them.
func labelSessionRows(out []process.SessionRow) {
	for i := range out {
		r := &out[i]
		r.LabelText, r.LabelSource = process.SessionLabel(r.SessionID, r.Title)
		r.LabelTextAlone, r.LabelSourceAlone = process.SessionLabel(r.SessionID, r.TitleAlone)
		r.Title, r.TitleAlone = nil, nil
	}
}

// sessionRowsUntitled is the rows with their own titles, not yet labelled.
func sessionRowsUntitled(rows Rows) []process.SessionRow {
	list := func(table string, f func(any) (string, string)) map[string][]string {
		m := map[string]map[string]bool{}
		for _, r := range rows[table] {
			s, v := f(r)
			if m[s] == nil {
				m[s] = map[string]bool{}
			}
			m[s][v] = true
		}
		out := map[string][]string{}
		for s, set := range m {
			for v := range set {
				out[s] = append(out[s], v)
			}
			sort.Strings(out[s])
		}
		return out
	}
	prompts := list(TableSessionPromptIDs, func(r any) (string, string) { x := r.(SessionPromptID); return x.SessionID, x.PromptID })
	devices := list(TableSessionDevices, func(r any) (string, string) { x := r.(SessionDevice); return x.SessionID, x.DeviceID })
	accounts := list(TableSessionCallAccounts, func(r any) (string, string) {
		x := r.(SessionCallAccount)
		return x.SessionID, x.AccountID
	})
	missed := map[string][]int64{}
	for _, r := range rows[TableSessionMissed] {
		x := r.(SessionMissed)
		missed[x.SessionID] = append(missed[x.SessionID], x.TSNS)
	}
	var edges []SessionEdge
	for _, r := range rows[TableSessionEdges] {
		edges = append(edges, r.(SessionEdge))
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		return a.ChildFirstNS < b.ChildFirstNS || a.ChildFirstNS == b.ChildFirstNS && a.ChildSessionID < b.ChildSessionID
	})
	kids, kinds := map[string][]string{}, map[string][]string{}
	for _, e := range edges {
		kids[e.ParentSessionID] = append(kids[e.ParentSessionID], e.ChildSessionID)
		kinds[e.ParentSessionID] = append(kinds[e.ParentSessionID], e.Kind)
	}

	var out []process.SessionRow
	for _, r := range rows[TableSessionRows] {
		k := r.(SessionRowK)
		x := k.row()
		x.PromptIDs, x.Devices, x.CallAccounts = prompts[k.SessionID], devices[k.SessionID], accounts[k.SessionID]
		if k.Events != nil {
			x.CovWindowFrom, x.CovWindowTo, x.CovWindowCount = process.CoverageWindows(missed[k.SessionID])
		}
		x.ChildrenJSON = process.SessionChildrenJSON(kids[k.SessionID], kinds[k.SessionID])
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// applyTitles gives the Codex threads the titles the rows' title threads name.
func applyTitles(rows Rows, out []process.SessionRow) {
	// Codex titles: a title thread's title call names the same account's Codex
	// thread (without a title of its own) that started last before it, within
	// five minutes, each in turn; alone, a thread's earliest such title.
	var titles []SessionTitleCall
	for _, r := range rows[TableSessionTitleCalls] {
		titles = append(titles, r.(SessionTitleCall))
	}
	sort.Slice(titles, func(i, j int) bool {
		a, b := titles[i], titles[j]
		return a.TSNS < b.TSNS || a.TSNS == b.TSNS && a.CallID < b.CallID
	})
	var codex []*process.SessionRow
	for i := range out {
		if r := &out[i]; r.Product == process.ProductCodex && r.Title == nil && r.ModelCalls > 0 {
			codex = append(codex, r)
		}
	}
	has := func(r *process.SessionRow, a string) bool {
		for _, x := range r.CallAccounts {
			if x == a {
				return true
			}
		}
		return false
	}
	for _, t := range titles {
		if t.Title == "" {
			continue
		}
		var pick *process.SessionRow
		for _, r := range codex {
			if has(r, t.AccountID) && 0 <= t.TSNS-r.StartNS && t.TSNS-r.StartNS <= process.TitleWithin {
				if r.TitleAlone == nil {
					r.TitleAlone = ptrTo(t.Title)
				}
				if r.Title == nil && (pick == nil || r.StartNS > pick.StartNS) {
					pick = r
				}
			}
		}
		if pick != nil {
			pick.Title = ptrTo(t.Title)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

// callToolUsesOf is the stored rows as the query reads them: distinct, ordered.
func CallToolUsesOf(rows Rows) []process.CallToolUse {
	seen := map[process.CallToolUse]bool{}
	var out []process.CallToolUse
	for _, r := range rows[TableCallToolUses] {
		x := r.(CallToolUse).CallToolUse
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sortCallTools(out)
	return out
}

func sortCallTools(out []process.CallToolUse) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
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
}

// rqView is the stored tables a query reads, folded across keys.
type rqView struct {
	txs      []ReqTx
	reports  map[string][]ReportRow // session → its reports
	missed   map[string][]MissedRow // session → its not-captured rows
	batches  map[string]*rqBatch
	last     map[string]int64
	creds    map[string][]string // session → its credentials
	email    map[string]string   // account → lower(email)
	sessOf   map[string]map[string]bool
	known    map[string]bool
	visible  []string
	index    map[string][]PromptIndex // session → its prompts' numbers
	turns    map[string][]CodexTurn   // session → its Codex turns' first calls
	sessRows map[string]process.SessionRow
}

type rqBatch struct {
	n                int64
	name             string
	session          string
	sessionNS        int64
	hasSess, hasName bool
}

func viewOf(rows Rows, sessions []process.SessionRow) *rqView {
	v := &rqView{reports: map[string][]ReportRow{}, missed: map[string][]MissedRow{}, batches: map[string]*rqBatch{},
		last: map[string]int64{}, creds: map[string][]string{}, email: map[string]string{},
		sessOf: map[string]map[string]bool{}, known: map[string]bool{}, index: map[string][]PromptIndex{},
		turns: map[string][]CodexTurn{}, sessRows: map[string]process.SessionRow{}}
	v.txs = tableRows[ReqTx](rows, TableRequestTxs)
	for _, r := range tableRows[ReportRow](rows, TableRequestReports) {
		v.reports[r.SessionID] = append(v.reports[r.SessionID], r)
	}
	for _, m := range tableRows[MissedRow](rows, TableRequestMissed) {
		v.missed[m.SessionID] = append(v.missed[m.SessionID], m)
	}
	for _, m := range tableRows[BatchMember](rows, TableBatchMembers) {
		b := v.batches[m.BatchID]
		if b == nil {
			b = &rqBatch{}
			v.batches[m.BatchID] = b
		}
		b.n++
		if m.Name != "" && (!b.hasName || m.Name < b.name) {
			b.name, b.hasName = m.Name, true
		}
		if m.SessionID != "" && (!b.hasSess || m.NS < b.sessionNS || m.NS == b.sessionNS && m.SessionID < b.session) {
			b.session, b.sessionNS, b.hasSess = m.SessionID, m.NS, true
		}
	}
	for _, l := range tableRows[SessionLast](rows, TableSessionLast) {
		v.last[l.SessionID] = l.LastNS
	}
	for _, c := range tableRows[SessionCredential](rows, TableSessionCredential) {
		v.creds[c.SessionID] = append(v.creds[c.SessionID], c.KeyHash)
	}
	for _, a := range tableRows[process.Account](rows, TableAccounts) {
		if a.Email != "" {
			v.email[a.AccountID] = strings.ToLower(a.Email)
		}
	}
	for _, sa := range tableRows[SessionAccount](rows, TableSessionAccounts) {
		if v.sessOf[sa.AccountID] == nil {
			v.sessOf[sa.AccountID] = map[string]bool{}
		}
		v.sessOf[sa.AccountID][sa.SessionID] = true
		v.known[sa.SessionID] = true
	}
	for _, sv := range tableRows[SessionVisible](rows, TableSessionVisible) {
		v.visible = append(v.visible, sv.SessionID)
	}
	for _, p := range tableRows[PromptIndex](rows, TablePromptIndex) {
		v.index[p.SessionID] = append(v.index[p.SessionID], p)
	}
	for _, c := range tableRows[CodexTurn](rows, TableCodexTurns) {
		v.turns[c.SessionID] = append(v.turns[c.SessionID], c)
	}
	for _, r := range sessions {
		v.sessRows[r.SessionID] = r
	}
	return v
}

type rqRep struct {
	ttft             *float64
	cache5m, cache1h *int64
}

// scopeRows is the rows of the transactions made with the sessions' credentials
// (with only, just that session's), and the sessions' not-captured calls.
func (v *rqView) scopeRows(sessions []string, only string) []process.RequestRow {
	in := map[string]bool{}
	for _, s := range sessions {
		in[s] = true
	}
	hashes := map[string]bool{}
	for _, s := range sessions {
		for _, h := range v.creds[s] {
			hashes[h] = true
		}
	}
	txSession := func(t ReqTx) string {
		if t.OwnSession != "" {
			return t.OwnSession
		}
		if b := v.batches[t.CallID]; b != nil && b.hasSess {
			return b.session
		}
		return ""
	}
	type tx struct {
		ReqTx
		session string
	}
	var txs []tx
	for _, t := range v.txs {
		if t.IsTx && t.KeyHash != "" && hashes[t.KeyHash] {
			s := txSession(t)
			if only == "" || s == only {
				txs = append(txs, tx{t, s})
			}
		}
	}
	// Prompt numbers: Claude Code's latest input prompt (time, key) across the
	// sessions; a Codex turn's place in its thread wins, the last session's.
	type pick struct {
		n   int64
		ts  int64
		key string
	}
	cc := map[string]pick{}
	for _, s := range sessions {
		for _, p := range v.index[s] {
			if cur, ok := cc[p.PromptID]; !ok || lessTsID(tsID{cur.ts, cur.key}, tsID{p.TS, p.Key}) {
				cc[p.PromptID] = pick{p.Index, p.TS, p.Key}
			}
		}
	}
	index := map[string]int64{}
	for pid, p := range cc {
		index[pid] = p.n
	}
	for _, s := range sessions {
		ts := append([]CodexTurn(nil), v.turns[s]...)
		sort.Slice(ts, func(i, j int) bool {
			return ts[i].FirstNS < ts[j].FirstNS || ts[i].FirstNS == ts[j].FirstNS && ts[i].PromptID < ts[j].PromptID
		})
		for n, c := range ts {
			index[c.PromptID] = int64(n + 1)
		}
	}
	// Each request id's first report.
	var reps []ReportRow
	for _, s := range sessions {
		reps = append(reps, v.reports[s]...)
	}
	sort.Slice(reps, func(i, j int) bool {
		return lessTsID(tsID{reps[i].TS, reps[i].EventKey}, tsID{reps[j].TS, reps[j].EventKey})
	})
	reports := map[string]rqRep{}
	for _, e := range reps {
		for _, k := range []string{e.ReqID, e.ClientReqID} {
			if _, ok := reports[k]; k != "" && !ok {
				reports[k] = rqRep{e.TTFT, e.Cache5m, e.Cache1h}
			}
		}
	}
	repOf := func(rid, crid string) *rqRep {
		if r, ok := reports[rid]; ok && rid != "" {
			return &r
		}
		if r, ok := reports[crid]; ok && crid != "" {
			return &r
		}
		return nil
	}
	sort.Slice(txs, func(i, j int) bool {
		a, b := txs[i].Row, txs[j].Row
		return a.TsNS < b.TsNS || a.TsNS == b.TsNS && a.RequestID < b.RequestID
	})
	steps, seen := map[string]int64{}, map[string]int64{}
	for _, t := range txs {
		if t.Kind == "model" && t.PromptID != "" && t.Hidden == "" {
			seen[t.PromptID]++
			steps[t.Row.RequestID] = seen[t.PromptID]
		}
	}
	var rows []process.RequestRow
	for _, t := range txs {
		r := t.Row
		r.ToolCalls = append([]string{}, r.ToolCalls...)
		r.SessionID = nzStr(t.session)
		if t.PromptID != "" {
			if n, ok := index[t.PromptID]; ok {
				r.PromptIndex = &n
			}
		}
		if t.Kind == "model" {
			if rep := repOf(t.ReqID, t.ClientReqID); rep != nil {
				r.TTFTMS, r.TokensCacheWrite5m, r.TokensCacheWrite1h = rep.ttft, rep.cache5m, rep.cache1h
			}
		}
		if n, ok := steps[r.RequestID]; ok {
			r.StepIndex = &n
		}
		if t.Kind == "analytics" {
			var n int64
			name := ""
			if b := v.batches[t.CallID]; b != nil {
				n, name = b.n, b.name
			}
			r.Detail = strconv.FormatInt(n, 10) + " events"
			if t.Product == canon.ProductCodex && name != "" {
				r.Detail = name
				if n != 1 {
					r.Detail = name + " · " + strconv.FormatInt(n, 10) + " events"
				}
			}
		}
		rows = append(rows, r)
	}
	for _, s := range sessions {
		ms := append([]MissedRow(nil), v.missed[s]...)
		sort.Slice(ms, func(i, j int) bool {
			return lessTsID(tsID{ms[i].Row.TsNS, ms[i].EventKey}, tsID{ms[j].Row.TsNS, ms[j].EventKey})
		})
		for _, m := range ms {
			r := m.Row
			r.ToolCalls = []string{}
			if rep := repOf(m.ReqID, m.ClientReqID); rep != nil {
				r.TTFTMS, r.TokensCacheWrite5m, r.TokensCacheWrite1h = rep.ttft, rep.cache5m, rep.cache1h
			}
			rows = append(rows, r)
		}
	}
	return rows
}

func nzStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sortRows(rows []process.RequestRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return a.TsNS < b.TsNS || a.TsNS == b.TsNS && a.RequestID < b.RequestID
	})
}

// sessionRows is a session's Requests tab: its own rows, and for a Codex thread
// its client's calls no thread names, from its start to a minute after its last.
func (v *rqView) sessionRows(sr process.SessionRow) []process.SessionRequestRow {
	s := sr.SessionID
	codex := sr.Product == canon.ProductCodex
	only := s
	if codex {
		only = ""
	}
	lo, hi := sr.StartNS/1000, (sr.LastNS+60*1_000_000_000)/1000
	client := ""
	if sr.Client != nil {
		client = *sr.Client
	}
	var kept []process.RequestRow
	for _, x := range v.scopeRows([]string{s}, only) {
		xc := ""
		if x.Client != nil {
			xc = *x.Client
		}
		if x.SessionID != nil && *x.SessionID == s ||
			codex && x.SessionID == nil && x.Agent != nil && *x.Agent == "codex" && x.Kind != "model" &&
				xc == client && lo <= x.TsNS/1000 && x.TsNS/1000 <= hi {
			kept = append(kept, x)
		}
	}
	sortRows(kept)
	out := make([]process.SessionRequestRow, len(kept))
	for i, x := range kept {
		out[i] = process.SessionRequestRow{Scope: s, Seq: int64(i), Row: x}
	}
	return out
}

func sortPresence(p []Presence) {
	sort.Slice(p, func(i, j int) bool {
		a, b := p[i], p[j]
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.FromNS < b.FromNS
	})
}
