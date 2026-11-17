package flush

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Write collapses recs and writes them in one transaction (§4). pod names the
// observer in ingest_pods ("": none). Re-running it changes nothing.
func Write(ctx context.Context, db *pgxpool.Pool, pod string, recs []records.Record) error {
	rows := Collapse(recs)
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return rows.write(ctx, tx, pod) })
}

// WriteTx is Write inside tx, for a worker loading one raw file and marking it
// loaded in the same transaction (docs/PIPELINE.md §2.2): key is the file, and
// ranges are its completed calls' members, written onto their calls rows.
func WriteTx(ctx context.Context, tx pgx.Tx, pod string, recs []records.Record, key string, ranges []records.RawRange) error {
	rows := Collapse(recs)
	at := make(map[string]records.RawRange, len(ranges))
	for _, r := range ranges {
		at[r.CallID] = r
	}
	for i := range rows.Calls {
		if r, ok := at[rows.Calls[i].R.CallID]; ok {
			rows.Calls[i].RawKey, rows.Calls[i].RawOffset, rows.Calls[i].RawLen = key, r.Offset, r.Len
		}
	}
	return rows.write(ctx, tx, pod)
}

func (r *Rows) write(ctx context.Context, tx pgx.Tx, pod string) error {
	var stmts []*insert

	calls := &insert{table: "calls"}
	cs := r.Calls
	col(calls, cs, "call_id", "text", func(c *Call) string { return c.R.CallID })
	col(calls, cs, "correlation_id", "text", func(c *Call) string { return c.R.CorrelationID })
	col(calls, cs, "session_id", "text", func(c *Call) *string { return str(c.R.SessionID) })
	col(calls, cs, "scope_session_id", "text", func(c *Call) *string { return str(c.Scope) })
	col(calls, cs, "prompt_id", "text", func(c *Call) *string { return str(c.R.PromptID) })
	col(calls, cs, "account_id", "text", func(c *Call) *string { return str(c.R.PersonID) })
	col(calls, cs, "device_id", "text", func(c *Call) *string { return str(c.R.DeviceID) })
	col(calls, cs, "org_id", "text", func(c *Call) *string { return str(c.R.OrgID) })
	col(calls, cs, "agent_id", "text", func(c *Call) *string { return str(c.R.AgentID) })
	col(calls, cs, "agent_type", "text", func(c *Call) *string { return str(c.R.AgentType) })
	col(calls, cs, "key_hash", "text", func(c *Call) *string { return str(c.R.KeyHash) })
	col(calls, cs, "key_hint", "text", func(c *Call) *string { return str(c.R.KeyHint) })
	col(calls, cs, "source", "text", func(c *Call) *string { return str(c.R.Product) })
	col(calls, cs, "client_mode", "text", func(c *Call) *string { return str(c.R.Client) })
	col(calls, cs, "client_version", "text", func(c *Call) *string { return str(c.R.ClientVersion) })
	col(calls, cs, "kind", "text", func(c *Call) string { return c.Kind })
	col(calls, cs, "request_class", "text", func(c *Call) *string { return str(c.R.RequestClass) })
	col(calls, cs, "host", "text", func(c *Call) *string { return str(c.R.Host) })
	col(calls, cs, "method", "text", func(c *Call) *string { return str(c.R.Method) })
	col(calls, cs, "path", "text", func(c *Call) *string { return str(c.R.Path) })
	col(calls, cs, "status", "bigint", func(c *Call) *int64 { return num(c.R.Status) })
	col(calls, cs, "error", "text", func(c *Call) *string { return str(c.R.Error) })
	col(calls, cs, "ip", "text", func(c *Call) *string { return str(c.R.IP) })
	col(calls, cs, "transport", "text", func(c *Call) *string { return str(c.R.Transport) })
	col(calls, cs, "ws_turn", "bigint", func(c *Call) *int64 { return num(c.R.WSTurn) })
	col(calls, cs, "start_ns", "bigint", func(c *Call) *int64 { return num(c.R.StartNS) })
	col(calls, cs, "response_start_ns", "bigint", func(c *Call) *int64 { return num(c.R.ResponseStartNS) })
	col(calls, cs, "end_ns", "bigint", func(c *Call) *int64 { return num(c.R.EndNS) })
	col(calls, cs, "model", "text", func(c *Call) *string { return str(c.R.Model) })
	col(calls, cs, "effort", "text", func(c *Call) *string { return str(c.R.Effort) })
	col(calls, cs, "stop_reason", "text", func(c *Call) *string { return str(c.R.StopReason) })
	col(calls, cs, "thread_type", "text", func(c *Call) *string { return str(c.R.ThreadType) })
	col(calls, cs, "stream", "boolean", func(c *Call) bool { return c.R.Stream })
	col(calls, cs, "service_tier", "text", func(c *Call) *string { return str(c.R.ServiceTier) })
	col(calls, cs, "input_tokens", "bigint", func(c *Call) *int64 { return num(c.R.InputTokens) })
	col(calls, cs, "output_tokens", "bigint", func(c *Call) *int64 { return num(c.R.OutputTokens) })
	col(calls, cs, "cache_read_tokens", "bigint", func(c *Call) *int64 { return num(c.R.CacheReadTokens) })
	col(calls, cs, "cache_write_tokens", "bigint", func(c *Call) *int64 { return num(c.R.CacheWriteTokens) })
	col(calls, cs, "reasoning_tokens", "bigint", func(c *Call) *int64 { return num(c.R.ReasoningTokens) })
	col(calls, cs, "request_bytes", "bigint", func(c *Call) *int64 { return num(c.R.RequestBytes) })
	col(calls, cs, "response_bytes", "bigint", func(c *Call) *int64 { return num(c.R.ResponseBytes) })
	col(calls, cs, "content_encoding", "text", func(c *Call) *string { return str(c.R.ContentEncoding) })
	col(calls, cs, "user_agent", "text", func(c *Call) *string { return str(c.R.UserAgent) })
	col(calls, cs, "tool_uses", "jsonb", func(c *Call) *string { return str(c.R.ToolUsesJSON) })
	col(calls, cs, "detail", "jsonb", func(c *Call) *string { return str(c.R.DetailJSON) })
	col(calls, cs, "client", "jsonb", func(c *Call) *string { return str(c.R.ClientJSON) })
	col(calls, cs, "provider", "jsonb", func(c *Call) *string { return str(c.R.ProviderJSON) })
	col(calls, cs, "ext", "jsonb", func(c *Call) *string { return str(c.R.ExtJSON) })
	col(calls, cs, "hidden_kind", "text", func(c *Call) *string { return str(c.R.HiddenKind) })
	col(calls, cs, "synthetic", "boolean", func(c *Call) bool { return c.R.Synthetic })
	col(calls, cs, "request_id", "text", func(c *Call) *string { return str(c.R.RequestID) })
	col(calls, cs, "client_request_id", "text", func(c *Call) *string { return str(c.R.ClientRequestID) })
	col(calls, cs, "message_id", "text", func(c *Call) *string { return str(c.R.MessageID) })
	col(calls, cs, "parent_message_id", "text", func(c *Call) *string { return str(c.R.PreviousMessageID) })
	col(calls, cs, "raw_key", "text", func(c *Call) *string { return str(c.RawKey) })
	col(calls, cs, "raw_offset", "bigint", func(c *Call) *int64 { return rawNum(c.RawKey, c.RawOffset) })
	col(calls, cs, "raw_len", "bigint", func(c *Call) *int64 { return rawNum(c.RawKey, c.RawLen) })
	// A start fills in nothing once there's an end; an end replaces a start, a
	// swept end, or an earlier end, keeping the start's time if it has none,
	// and its place in a raw file if it names none.
	calls.conflict = "ON CONFLICT (call_id) DO UPDATE SET " + calls.excluded("call_id", "start_ns", "raw_key", "raw_offset", "raw_len") +
		", start_ns = coalesce(excluded.start_ns, calls.start_ns), swept = false" +
		", raw_key = coalesce(excluded.raw_key, calls.raw_key), raw_offset = coalesce(excluded.raw_offset, calls.raw_offset)" +
		", raw_len = coalesce(excluded.raw_len, calls.raw_len)" +
		" WHERE excluded.end_ns IS NOT NULL AND (calls.end_ns IS NULL OR calls.swept OR excluded.end_ns > calls.end_ns)"
	stmts = append(stmts, calls)

	accts := &insert{table: "accounts"}
	as := r.Accounts
	col(accts, as, "account_id", "text", func(a *Account) string { return a.ID })
	newestCols(accts, as, "email", func(a *Account) Newest { return a.Email })
	newestCols(accts, as, "name", func(a *Account) Newest { return a.Name })
	newestCols(accts, as, "org", func(a *Account) Newest { return a.Org })
	newestCols(accts, as, "codex_plan", func(a *Account) Newest { return a.CodexP })
	col(accts, as, "seen_ns", "bigint", func(a *Account) int64 { return a.Seen })
	accts.conflict = "ON CONFLICT (account_id) DO UPDATE SET " + newestSet("accounts", "email", "name", "org", "codex_plan") +
		", seen_ns = greatest(accounts.seen_ns, excluded.seen_ns)"
	stmts = append(stmts, accts)

	for _, s := range []struct {
		table, b string
		rows     []Span
	}{{"session_accounts", "account_id", r.SessionAccounts}, {"session_credentials", "key_hash", r.SessionCredentials}} {
		in := &insert{table: s.table}
		col(in, s.rows, "session_id", "text", func(x *Span) string { return x.A })
		col(in, s.rows, s.b, "text", func(x *Span) string { return x.B })
		col(in, s.rows, "first_ns", "bigint", func(x *Span) int64 { return x.First })
		col(in, s.rows, "last_ns", "bigint", func(x *Span) int64 { return x.Last })
		in.conflict = fmt.Sprintf("ON CONFLICT (session_id, %[2]s) DO UPDATE SET first_ns = least(%[1]s.first_ns, excluded.first_ns), "+
			"last_ns = greatest(%[1]s.last_ns, excluded.last_ns)", s.table, s.b)
		stmts = append(stmts, in)
	}

	devs := &insert{table: "account_devices"}
	ds := r.Devices
	col(devs, ds, "account_id", "text", func(d *Device) string { return d.Account })
	col(devs, ds, "device_id", "text", func(d *Device) string { return d.Device })
	newestCols(devs, ds, "agent", func(d *Device) Newest { return d.Agent })
	newestCols(devs, ds, "version", func(d *Device) Newest { return d.Version })
	col(devs, ds, "first_ns", "bigint", func(d *Device) int64 { return d.First })
	col(devs, ds, "last_ns", "bigint", func(d *Device) int64 { return d.Last })
	devs.conflict = "ON CONFLICT (account_id, device_id) DO UPDATE SET " +
		newestSet("account_devices", "agent", "version") +
		", first_ns = least(account_devices.first_ns, excluded.first_ns), last_ns = greatest(account_devices.last_ns, excluded.last_ns)"
	stmts = append(stmts, devs)

	parents := &insert{table: "session_edges"}
	ps := r.Parents
	col(parents, ps, "child_session_id", "text", func(p *Parent) string { return p.Child })
	col(parents, ps, "parent_session_id", "text", func(p *Parent) string { return p.Parent })
	col(parents, ps, "kind", "text", func(p *Parent) string { return p.Kind })
	col(parents, ps, "first_ns", "bigint", func(p *Parent) int64 { return p.First })
	parents.conflict = "ON CONFLICT (child_session_id, parent_session_id) DO UPDATE SET " +
		"kind = least(session_edges.kind, excluded.kind), first_ns = least(session_edges.first_ns, excluded.first_ns)"
	stmts = append(stmts, parents)

	// Interceptor verdicts: a verdict is written once and never changes.
	vs := &insert{table: "verdicts"}
	vrows := r.Verdicts
	col(vs, vrows, "call_id", "text", func(v *Verdict) string { return v.CallID })
	col(vs, vrows, "seq", "bigint", func(v *Verdict) int64 { return v.V.Seq })
	col(vs, vrows, "ts_ns", "bigint", func(v *Verdict) int64 { return v.V.NS })
	col(vs, vrows, "callback", "text", func(v *Verdict) string { return v.V.Callback })
	col(vs, vrows, "action", "text", func(v *Verdict) string { return v.V.Action })
	col(vs, vrows, "applied", "boolean", func(v *Verdict) bool { return v.V.Applied })
	col(vs, vrows, "mode", "text", func(v *Verdict) *string { return str(v.V.Mode) })
	col(vs, vrows, "message_id", "text", func(v *Verdict) *string { return str(v.V.MessageID) })
	col(vs, vrows, "respond_mode", "text", func(v *Verdict) *string { return str(v.V.RespondMode) })
	col(vs, vrows, "reason", "text", func(v *Verdict) *string { return str(v.V.Reason) })
	col(vs, vrows, "annotations", "jsonb", func(v *Verdict) *string { return str(v.V.AnnotationsJSON) })
	col(vs, vrows, "latency_ms", "bigint", func(v *Verdict) int64 { return v.V.LatencyMS })
	// At most records.MaxVerdictsPerCall a call, whatever wrote the rest.
	vs.conflict = fmt.Sprintf("WHERE (SELECT count(*) FROM verdicts v WHERE v.call_id = u.call_id) < %d ON CONFLICT (call_id, seq) DO NOTHING",
		records.MaxVerdictsPerCall)
	stmts = append(stmts, vs)
	for _, s := range stmts {
		if s.n == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, s.sql(), s.vals...); err != nil {
			return fmt.Errorf("flush: %s: %w", s.table, err)
		}
	}
	if pod != "" && r.LastNS > 0 { // as the tail writes it (tail/tail.go)
		if _, err := tx.Exec(ctx, `INSERT INTO ingest_pods (incarnation, last_ns, at) VALUES ($1, $2, now())
			ON CONFLICT (incarnation) DO UPDATE SET last_ns = greatest(ingest_pods.last_ns, EXCLUDED.last_ns),
				at = greatest(ingest_pods.at, EXCLUDED.at)`, pod, r.LastNS); err != nil {
			return fmt.Errorf("flush: ingest_pods: %w", err)
		}
	}

	// A call carrying back tool calls' results marks the steps of the calls
	// that made them: a step's exit codes are its session's first result for
	// each of its tool calls, and that can be a later call's (§5.2 steps). One
	// lookup per id through the index on detail's calls.
	dirty := r.Dirty
	if len(r.ResultIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT DISTINCT c.call_id FROM unnest($1::text[]) k,
			LATERAL (SELECT c.call_id, c.prompt_id FROM calls c WHERE c.detail -> 'calls' @> jsonb_build_array(jsonb_build_object('id', k)) OFFSET 0) c
			WHERE c.prompt_id IS NOT NULL`, r.ResultIDs)
		if err != nil {
			return fmt.Errorf("flush: results: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("flush: results: %w", err)
		}
		for _, id := range ids {
			dirty = append(dirty, Dirty{"step", id})
		}
	}
	if len(dirty) > len(r.Dirty) {
		slices.SortFunc(dirty, func(a, b Dirty) int {
			if c := strings.Compare(a.Kind, b.Kind); c != 0 {
				return c
			}
			return strings.Compare(a.Key, b.Key)
		})
		dirty = slices.Compact(dirty)
	}
	if len(dirty) > 0 {
		d := &insert{table: "dirty"}
		col(d, dirty, "kind", "text", func(x *Dirty) string { return x.Kind })
		col(d, dirty, "key", "text", func(x *Dirty) string { return x.Key })
		d.conflict = "ON CONFLICT (kind, key) DO UPDATE SET seq = excluded.seq"
		if _, err := tx.Exec(ctx, d.sql(), d.vals...); err != nil {
			return fmt.Errorf("flush: dirty: %w", err)
		}
	}
	return nil
}

// insert is one INSERT … SELECT FROM unnest(…) ON CONFLICT: a table's
// columns, each sent as one array.
type insert struct {
	table    string
	n        int
	cols     []string
	types    []string // the column's type; jsonb is sent as text and cast
	vals     []any
	conflict string
}

// col adds a column whose value for each row is f's.
func col[R, V any](in *insert, rows []R, name, typ string, f func(*R) V) {
	vs := make([]V, len(rows))
	for i := range rows {
		vs[i] = f(&rows[i])
	}
	in.n = len(rows)
	in.cols, in.types, in.vals = append(in.cols, name), append(in.types, typ), append(in.vals, vs)
}

// newestCols adds a newest-wins value and its time (NULL for none).
func newestCols[R any](in *insert, rows []R, name string, f func(*R) Newest) {
	col(in, rows, name, "text", func(r *R) *string { return str(f(r).V) })
	col(in, rows, name+"_ns", "bigint", func(r *R) *int64 {
		if n := f(r); n.V != "" {
			return &n.NS
		}
		return nil
	})
}

// newestSet is the SET for newest-wins columns, as Newest.offer decides.
func newestSet(table string, names ...string) string {
	var out []string
	for _, f := range names {
		newer := fmt.Sprintf("excluded.%[2]s IS NOT NULL AND (%[1]s.%[2]s IS NULL OR excluded.%[2]s_ns > %[1]s.%[2]s_ns OR "+
			"excluded.%[2]s_ns = %[1]s.%[2]s_ns AND excluded.%[2]s > %[1]s.%[2]s)", table, f)
		out = append(out, fmt.Sprintf("%[2]s = CASE WHEN %[3]s THEN excluded.%[2]s ELSE %[1]s.%[2]s END, "+
			"%[2]s_ns = CASE WHEN %[3]s THEN excluded.%[2]s_ns ELSE %[1]s.%[2]s_ns END", table, f, newer))
	}
	return strings.Join(out, ", ")
}

// excluded is "c = excluded.c" for every column but skip.
func (in *insert) excluded(skip ...string) string {
	var out []string
	for _, c := range in.cols {
		if !slices.Contains(skip, c) {
			out = append(out, c+" = excluded."+c)
		}
	}
	return strings.Join(out, ", ")
}

func (in *insert) sql() string {
	args, sel := make([]string, len(in.cols)), make([]string, len(in.cols))
	for i, c := range in.cols {
		sent, x := in.types[i], "u."+c
		switch sent {
		case "jsonb":
			sent, x = "text", x+"::jsonb"
		}
		args[i], sel[i] = fmt.Sprintf("$%d::%s[]", i+1, sent), x
	}
	cols := strings.Join(in.cols, ", ")
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s) %s",
		in.table, cols, strings.Join(sel, ", "), strings.Join(args, ", "), cols, in.conflict)
}

func str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// rawNum is a raw range's offset or length, NULL without a raw file (an
// offset of 0 is a real one).
func rawNum(key string, n int64) *int64 {
	if key == "" {
		return nil
	}
	return &n
}

func num(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

// Tables are the tables a flush writes facts to, and the dirty list.
var Tables = []string{"calls", "accounts", "session_accounts", "session_credentials", "account_devices", "session_edges", "dirty"}

// Summary is a table's row count, a checksum of its rows (independent of their
// order: the sum of a hash of each row's text; dirty's of its kind and key
// alone) and its size on disk with indexes and TOAST.
type Summary struct {
	Table    string
	Rows     int64
	Checksum string
	Bytes    int64
}

// Summarize is each of Tables' Summary.
func Summarize(ctx context.Context, q pg.Querier) ([]Summary, error) {
	var out []Summary
	for _, t := range Tables {
		row := "t::text"
		switch t {
		case "dirty":
			row = "t.kind || '/' || t.key"
		case "calls": // a call's place in its raw file is a pointer, not a fact: loads of the same calls from other files agree
			row = "(to_jsonb(t) - 'raw_key' - 'raw_offset' - 'raw_len')::text"
		}
		s := Summary{Table: t}
		err := q.QueryRow(ctx, fmt.Sprintf(`SELECT count(*), coalesce(sum(('x' || substr(md5(%s), 1, 15))::bit(60)::bigint), 0)::text,
			pg_total_relation_size('%s') FROM %s t`, row, t, t)).Scan(&s.Rows, &s.Checksum, &s.Bytes)
		if err != nil {
			return nil, fmt.Errorf("summarize %s: %w", t, err)
		}
		out = append(out, s)
	}
	return out, nil
}
