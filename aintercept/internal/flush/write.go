package flush

import (
	"context"
	"encoding/json"
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
	col(calls, cs, "batch_events", "bigint", func(c *Call) *int64 { return num(c.BatchEvents) })
	col(calls, cs, "batch_name", "text", func(c *Call) *string { return str(c.BatchName) })
	col(calls, cs, "batch_other_sessions", "jsonb", func(c *Call) *string { return jsonList(c.BatchOther) })
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

	evs := &insert{table: "events"}
	es := r.Events
	col(evs, es, "event_key", "text", func(e *Event) string { return e.Key })
	col(evs, es, "batch_call_id", "text", func(e *Event) string { return e.Batch })
	col(evs, es, "batch_n", "bigint", func(e *Event) int64 { return e.N })
	col(evs, es, "batch_cc_n", "bigint", func(e *Event) *int64 { return num(e.CCN) })
	col(evs, es, "session_id", "text", func(e *Event) *string { return str(e.E.SessionID) })
	col(evs, es, "prompt_id", "text", func(e *Event) *string { return str(e.E.PromptID) })
	col(evs, es, "prompt_index", "bigint", func(e *Event) *int64 { return num(e.E.PromptIndex) })
	col(evs, es, "request_id", "text", func(e *Event) *string { return str(e.E.RequestID) })
	col(evs, es, "client_request_id", "text", func(e *Event) *string { return str(e.E.ClientRequestID) })
	col(evs, es, "tool_use_id", "text", func(e *Event) *string { return str(e.E.ToolUseID) })
	col(evs, es, "device_id", "text", func(e *Event) *string { return str(e.E.DeviceID) })
	col(evs, es, "account_id", "text", func(e *Event) *string { return str(e.E.PersonID) })
	col(evs, es, "org_id", "text", func(e *Event) *string { return str(e.E.OrgID) })
	col(evs, es, "agent_id", "text", func(e *Event) *string { return str(e.E.AgentID) })
	col(evs, es, "agent_type", "text", func(e *Event) *string { return str(e.E.AgentType) })
	col(evs, es, "key_hash", "text", func(e *Event) *string { return str(e.KeyHash) })
	col(evs, es, "source", "text", func(e *Event) string { return e.E.Source })
	col(evs, es, "name", "text", func(e *Event) *string { return str(e.E.Name) })
	col(evs, es, "type", "text", func(e *Event) *string { return str(e.E.Type) })
	col(evs, es, "client_event_id", "text", func(e *Event) *string { return str(e.E.ClientEventID) })
	col(evs, es, "model", "text", func(e *Event) *string { return str(e.E.Model) })
	col(evs, es, "permission_mode", "text", func(e *Event) *string { return str(e.E.PermissionMode) })
	col(evs, es, "head_sha", "text", func(e *Event) *string { return str(e.E.HeadSHA) })
	col(evs, es, "ts_ns", "bigint", func(e *Event) int64 { return e.E.NS })
	col(evs, es, "row_ns", "bigint", func(e *Event) int64 { return e.E.RowNS })
	col(evs, es, "seq", "bigint", func(e *Event) int64 { return e.E.Seq })
	col(evs, es, "metadata", "jsonb", func(e *Event) *string { return str(e.E.MetadataJSON) })
	col(evs, es, "env_hash", "envhash", func(e *Event) *string { return str(e.E.EnvJSON) })
	col(evs, es, "summary", "text", func(e *Event) string { return e.Summary })
	col(evs, es, "internal", "boolean", func(e *Event) bool { return e.Internal })
	col(evs, es, "marker", "text", func(e *Event) *string { return str(e.Marker) })
	// A resent batch counts once (owner decision 4): the earliest copy is kept,
	// whichever arrives first.
	evs.conflict = "ON CONFLICT (event_key) DO UPDATE SET " + evs.excluded("event_key") +
		" WHERE (excluded.row_ns, excluded.batch_call_id, excluded.batch_n) < (events.row_ns, events.batch_call_id, events.batch_n)"
	stmts = append(stmts, evs)

	accts := &insert{table: "accounts"}
	as := r.Accounts
	col(accts, as, "account_id", "text", func(a *Account) string { return a.ID })
	newestCols(accts, as, "email", func(a *Account) Newest { return a.Email })
	newestCols(accts, as, "name", func(a *Account) Newest { return a.Name })
	newestCols(accts, as, "org", func(a *Account) Newest { return a.Org })
	newestCols(accts, as, "plan", func(a *Account) Newest { return a.Plan })
	newestCols(accts, as, "codex_plan", func(a *Account) Newest { return a.CodexP })
	col(accts, as, "seen_ns", "bigint", func(a *Account) int64 { return a.Seen })
	accts.conflict = "ON CONFLICT (account_id) DO UPDATE SET " + newestSet("accounts", "email", "name", "org", "plan", "codex_plan") +
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
	col(devs, ds, "on_events", "boolean", func(d *Device) bool { return d.OnEvents })
	newestCols(devs, ds, "platform", func(d *Device) Newest { return d.Platform })
	newestCols(devs, ds, "terminal", func(d *Device) Newest { return d.Terminal })
	newestCols(devs, ds, "shell", func(d *Device) Newest { return d.Shell })
	newestCols(devs, ds, "agent", func(d *Device) Newest { return d.Agent })
	newestCols(devs, ds, "version", func(d *Device) Newest { return d.Version })
	col(devs, ds, "first_ns", "bigint", func(d *Device) int64 { return d.First })
	col(devs, ds, "last_ns", "bigint", func(d *Device) int64 { return d.Last })
	devs.conflict = "ON CONFLICT (account_id, device_id) DO UPDATE SET on_events = account_devices.on_events OR excluded.on_events, " +
		newestSet("account_devices", "platform", "terminal", "shell", "agent", "version") +
		", first_ns = least(account_devices.first_ns, excluded.first_ns), last_ns = greatest(account_devices.last_ns, excluded.last_ns)"
	stmts = append(stmts, devs)

	parents := &insert{table: "session_parents"}
	ps := r.Parents
	col(parents, ps, "child_session_id", "text", func(p *Parent) string { return p.Child })
	col(parents, ps, "parent_session_id", "text", func(p *Parent) string { return p.Parent })
	col(parents, ps, "kind", "text", func(p *Parent) string { return p.Kind })
	col(parents, ps, "first_ns", "bigint", func(p *Parent) int64 { return p.First })
	parents.conflict = "ON CONFLICT (child_session_id, parent_session_id) DO UPDATE SET " +
		"kind = least(session_parents.kind, excluded.kind), first_ns = least(session_parents.first_ns, excluded.first_ns)"
	stmts = append(stmts, parents)

	// Each distinct environment once, before the events that point at it.
	if envs := distinctEnvs(es); len(envs) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO event_envs (env_hash, env)
			SELECT `+envHash("u.env")+`, u.env::jsonb FROM unnest($1::text[]) AS u(env) ORDER BY 1
			ON CONFLICT (env_hash) DO NOTHING`, envs); err != nil {
			return fmt.Errorf("flush: event_envs: %w", err)
		}
	}
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

	// A model call marks the minutes of its reports, so a report that came
	// first stops counting as missed (§4.3); a report marks its calls' minutes,
	// so a call whose report came later stops counting as unreported.
	dirty := r.Dirty
	if len(r.ReportIDs)+len(r.ReportedIDs) > 0 {
		for _, q := range []struct {
			ids []string
			sql string
		}{
			{r.ReportIDs, `SELECT DISTINCT session_id || '/' || (ts_ns - ts_ns % 60000000000) FROM events
				WHERE session_id IS NOT NULL AND (request_id = ANY($1) OR client_request_id = ANY($1))`},
			{r.ReportedIDs, `SELECT DISTINCT session_id || '/' || (coalesce(start_ns, end_ns) - coalesce(start_ns, end_ns) % 60000000000)
				FROM calls WHERE session_id IS NOT NULL AND (request_id = ANY($1) OR client_request_id = ANY($1))`},
		} {
			if len(q.ids) == 0 {
				continue
			}
			rows, err := tx.Query(ctx, q.sql, q.ids)
			if err != nil {
				return fmt.Errorf("flush: reports: %w", err)
			}
			keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return fmt.Errorf("flush: reports: %w", err)
			}
			for _, k := range keys {
				dirty = append(dirty, Dirty{"minute", k})
			}
		}
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
		case "envhash": // JSON sent as text, stored as its event_envs key
			sent, x = "text", envHash(x)
		}
		args[i], sel[i] = fmt.Sprintf("$%d::%s[]", i+1, sent), x
	}
	cols := strings.Join(in.cols, ", ")
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s) %s",
		in.table, cols, strings.Join(sel, ", "), strings.Join(args, ", "), cols, in.conflict)
}

// envHash is the event_envs key of JSON text: the sha256 of its canonical
// form (jsonb's text), so equal environments written differently share a row.
func envHash(text string) string {
	return "sha256(convert_to((" + text + ")::jsonb::text, 'UTF8'))"
}

// distinctEnvs is the events' environments, each once.
func distinctEnvs(es []Event) []string {
	var out []string
	for i := range es {
		if es[i].E.EnvJSON != "" {
			out = append(out, es[i].E.EnvJSON)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
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

func jsonList(ss []string) *string {
	if len(ss) == 0 {
		return nil
	}
	b, _ := json.Marshal(ss)
	s := string(b)
	return &s
}

// Tables are the tables a flush writes facts to, and the dirty list.
var Tables = []string{"calls", "event_envs", "events", "accounts", "session_accounts", "session_credentials", "account_devices", "session_parents", "dirty"}

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
