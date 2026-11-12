package state

import (
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The event tables and the lookups their rows join.
const (
	TableEvents       = "event_rows"
	TableSessionTools = "session_tools"
	// TableRequestLinks is each request id's call in the session: the newest
	// visible call carrying it (by time, then id), and its label. An event
	// linking to a request id with none links to "reported:<id>".
	TableRequestLinks = "session_request_links"
	// TablePromptIndex is each prompt's number: a Claude Code prompt's from its
	// latest tengu_input_prompt (by time, then key).
	TablePromptIndex = "prompt_index"
	// TableCodexTurns is each Codex turn's first visible call: the query numbers
	// the thread's turns by it (dense_rank), and that number wins.
	TableCodexTurns = "codex_turns"
)

// Event is one Claude Code event's row (process EventRow) in its stored shape:
// its place is (TsNS, BatchID, then its order in the batch: Seq, RowNS, name,
// metadata, key), numbered by the query; its prompt's number and a request or
// prompt link's target and label are joined from the lookups above. LinkID is
// the request id for a request link.
type Event struct {
	process.EventRow
	BatchID  string `parquet:"batch_id"`
	N        int64  `parquet:"n"` // its place among all the batch's events, as ingest numbered it
	PromptID string `parquet:"prompt_id,optional"`
	// The batch order the reference numbers by (process batchKey): two events at
	// the same time in the same batch are ordered by these.
	EvSeq    int64  `parquet:"ev_seq"`
	RowNS    int64  `parquet:"row_ns"`
	OrderMD  string `parquet:"order_md"`
	EventKey string `parquet:"event_key"`
}

// RequestLink is a request id's call (TableRequestLinks).
type RequestLink struct {
	SessionID, RequestID, CallID, Label string
}

// PromptIndex is a Claude Code prompt's number (TablePromptIndex).
type PromptIndex struct {
	SessionID, PromptID string
	Index               int64
	TS                  int64
	Key                 string // the event it's from: the latest wins
}

// CodexTurn is a Codex turn's first visible call (TableCodexTurns).
type CodexTurn struct {
	SessionID, PromptID string
	FirstNS             int64
}

// eventsState is the key's indexes for the event tables.
type eventsState struct {
	links    map[string]*omap[tsID, RequestLink] // request id → its visible calls, by (time, id)
	callLink map[string]linkAt                   // a call's place in links, to take it back
	tools    map[string]int64                    // session tool → first time
	turns    map[string]int64                    // Codex prompt → first visible call
}

type linkAt struct {
	rid string
	at  tsID
}

func newEventsState() *eventsState {
	return &eventsState{links: map[string]*omap[tsID, RequestLink]{}, callLink: map[string]linkAt{},
		tools: map[string]int64{}, turns: map[string]int64{}}
}

func (s *KeyState) events(r records.Record, d *Delta) {
	sid := sessionOf(s.Key)
	switch {
	case r.Kind == records.KindClientEvent && r.Event != nil:
		e := r.Event
		if e.Source != canon.ProductClaudeCode || e.SessionID == "" || e.SessionID != sid {
			return
		}
		f := process.EventFactsOf(*e)
		row := Event{EventRow: f.Row, BatchID: e.CorrelationID, N: r.BatchN, PromptID: f.Prompt,
			EvSeq: e.Seq, RowNS: e.RowNS, OrderMD: e.MetadataJSON, EventKey: e.EventKey}
		row.EventID = e.CorrelationID + ":" + strconv.FormatInt(r.BatchN, 10)
		switch {
		case f.Row.Name == "tengu_event_loop_stall" && f.Device != "":
			row.LinkKind, row.LinkID, row.LinkLabel = "device", f.Device, "device"
		case f.Tool != "":
			row.LinkKind, row.LinkID, row.LinkLabel = "tool", f.Tool, firstStr(f.ToolName, "tool")
		case f.ReqID != "":
			row.LinkKind, row.LinkID = "request", f.ReqID
		case f.Prompt != "":
			row.LinkKind, row.LinkID = "prompt", f.Prompt
		default:
			row.LinkKind, row.LinkID, row.LinkLabel = "session", sid, "session"
		}
		d.Cases = append(d.Cases, CaseEventRow+"."+row.LinkKind)
		s.rows.put(d, TableEvents, "e:"+e.EventKey, row)
		if f.UsedTool != "" {
			switch {
			case f.UsedServer != "":
				s.useTool(d, sid, "mcp", f.UsedServer, f.UsedTool, e.NS)
			case !f.UsedIsMCP:
				s.useTool(d, sid, "builtin", "", f.UsedTool, e.NS)
			}
		}
		if pid, n, ok := process.PromptIndexOf(*e); ok {
			cur, has := s.rows[TablePromptIndex]["pi:"+pid]
			if !has || lessTsID(tsID{cur.(PromptIndex).TS, cur.(PromptIndex).Key}, tsID{e.NS, e.EventKey}) {
				d.Cases = append(d.Cases, CasePromptIndex)
				s.rows.put(d, TablePromptIndex, "pi:"+pid, PromptIndex{sid, pid, n, e.NS, e.EventKey})
			}
		}
	case r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection:
		c := r.Call
		s.unlink(d, sid, c.CallID)
		if c.SessionID != "" && c.SessionID == sid {
			for _, u := range process.ToolUses(c.ToolUsesJSON) {
				switch kind, server, tool := u[0], u[1], u[2]; {
				case kind == "mcp" && server != "" && tool != "":
					s.useTool(d, sid, "mcp", server, tool, firstNZ(c.StartNS, c.EndNS))
				case tool != "":
					s.useTool(d, sid, "builtin", "", tool, firstNZ(c.StartNS, c.EndNS))
				}
			}
		}
		if !visibleEnded(c) || c.SessionID != sid {
			return
		}
		ts := firstNZ(c.StartNS, c.EndNS)
		id := strings.ReplaceAll(c.CallID, "#", "~")
		if c.RequestID != "" {
			s.link(d, sid, c.CallID, c.RequestID, tsID{ts, id}, RequestLink{sid, c.RequestID, id, process.CallLabel(c.RequestClass, c.Model, c.StopReason)})
		}
		if c.Product == canon.ProductCodex && c.PromptID != "" {
			if at, ok := s.ev.turns[c.PromptID]; !ok || ts < at {
				d.Cases = append(d.Cases, CaseCodexTurn)
				s.ev.turns[c.PromptID] = ts
				s.rows.put(d, TableCodexTurns, "ct:"+c.PromptID, CodexTurn{sid, c.PromptID, ts})
			}
		}
	case r.Kind == records.KindCallStart:
		c := r.Call
		if s.ses.ended[c.CallID] || !visibleStart(c) || c.SessionID != sid || c.RequestID == "" {
			return
		}
		s.link(d, sid, c.CallID, c.RequestID, tsID{c.StartNS, c.CallID}, RequestLink{sid, c.RequestID, c.CallID, process.CallLabel(c.RequestClass, "", "")})
	}
}

func firstStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// useTool keeps a session tool's first time.
func (s *KeyState) useTool(d *Delta, sid, kind, server, tool string, ts int64) {
	k := kind + "\x00" + server + "\x00" + tool
	if cur, ok := s.ev.tools[k]; ok && cur <= ts {
		return
	}
	d.Cases = append(d.Cases, CaseSessionTool)
	s.ev.tools[k] = ts
	s.rows.put(d, TableSessionTools, "t:"+k, process.SessionTool{SessionID: sid, Kind: kind, Server: server, Tool: tool, FirstNS: ts})
}

// link puts a visible call under its request id; the newest is the link.
func (s *KeyState) link(d *Delta, sid, callID, rid string, at tsID, l RequestLink) {
	m := s.ev.links[rid]
	if m == nil {
		m = newOmap[tsID, RequestLink](lessTsID)
		s.ev.links[rid] = m
	}
	m.Set(at, l)
	s.ev.callLink[callID] = linkAt{rid, at}
	d.Cases = append(d.Cases, CaseRequestLink)
	s.relink(d, rid)
}

// unlink takes back a call's place under its request id (its start, now that
// its ending is here).
func (s *KeyState) unlink(d *Delta, sid, callID string) {
	la, ok := s.ev.callLink[callID]
	if !ok {
		return
	}
	delete(s.ev.callLink, callID)
	s.ev.links[la.rid].Delete(la.at)
	s.relink(d, la.rid)
}

func (s *KeyState) relink(d *Delta, rid string) {
	m := s.ev.links[rid]
	if m.Len() == 0 {
		s.rows.drop(d, TableRequestLinks, "rq:"+rid)
		return
	}
	c := m.chunks[len(m.chunks)-1].e
	s.rows.put(d, TableRequestLinks, "rq:"+rid, c[len(c)-1].v)
}
