package pg

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The schema's tables (docs/PIPELINE.md §3), from which schema.sql is
// generated: a data table's columns are its Go row type's, today's names and
// types (process/tables.go, state.TableTypes), so the rows the workers write
// and the tables the API loads can't drift from the code that builds them.

// Kind is what a table holds.
type Kind int

const (
	// Coordination tables: the key rows, the flags, the workers, the tail (§3.2).
	Coordination Kind = iota
	// Rendered tables: a session's rows of state.RenderSession, written by its
	// own key, keyed by the session (§3.3).
	Rendered
	// Stored tables: the state's own rows, keyed by (key, the state's row id)
	// (§3.4). The id is bytea: the state joins its parts with NUL.
	Stored
	// Folds: the directory folds and the batch parts (§3.4, §3.5).
	Fold
)

// Column is one column of a table.
type Column struct {
	Name    string
	Type    string // its SQL type
	NotNull bool
	// Field is the row type's field it's read from (nil: an extra column the
	// writer fills itself).
	Field []int
	// Optional is a field tagged `optional`: its zero value is NULL, as the
	// table codec writes it (and the oracle reads it).
	Optional bool
}

// Table is one table of the schema.
type Table struct {
	Name string
	Kind Kind
	// State is the state table a Stored table's rows come from (state.Table*).
	State string
	// Row is a data table's Go row type; Columns starts with its columns.
	Row     reflect.Type
	Columns []Column
	PK      []string
	// Indexes are each index's columns, and its predicate for a partial one:
	// "(a, b)" or "(a) WHERE …".
	Indexes []string
	// With is the table's storage parameters; LZ4 its columns compressed with lz4.
	With string
	LZ4  []string
	// Seed is a statement that fills the table once it's created.
	Seed string
}

// Column returns the named column (false: none).
func (t *Table) Column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

// Tables is every table of the schema, in schema.sql's order.
var Tables = buildTables()

// TableNamed returns the named table (nil: none).
func TableNamed(name string) *Table {
	for i := range Tables {
		if Tables[i].Name == name {
			return &Tables[i]
		}
	}
	return nil
}

// col is an extra column.
func col(name, typ string) Column { return Column{Name: name, Type: typ} }

func notNull(name, typ string) Column { return Column{Name: name, Type: typ, NotNull: true} }

// rowOpts shape how a row type becomes columns.
type rowOpts struct {
	omit   map[string]bool // columns not stored
	inline map[string]bool // nested structs whose columns aren't prefixed with its name
}

func buildTables() []Table {
	typ := func(v any) reflect.Type { return reflect.TypeOf(v) }
	var ts []Table
	add := func(t Table) { ts = append(ts, t) }

	// §3.2 Coordination. keys.day is DayOf's YYYY-MM-DD ("" for an a: key), as session_rows.key_day.
	add(Table{Name: "schema_version", Columns: []Column{notNull("hash", "text")}})
	add(Table{Name: "keys", PK: []string{"key"}, Columns: []Column{
		notNull("key", "text"), notNull("n", "bigint"), col("state", "text[]"), col("state_levels", "integer[]"),
		col("last_applied", "text"), col("grace", "text[]"), col("garbage", "jsonb"), col("render_version", "integer"),
		col("updated_ns", "bigint"), col("newest_row_ns", "bigint"), col("expires_ns", "bigint"), col("day", "text"),
		col("first_request_ns", "bigint"), col("earliest_ns", "bigint"), col("content_retained", "boolean"),
	}, Indexes: []string{
		"(day)", "(newest_row_ns)", "(expires_ns) WHERE expires_ns > 0", "(first_request_ns) WHERE first_request_ns > 0",
		"(earliest_ns) WHERE earliest_ns > 0", "(content_retained) WHERE content_retained",
	}})
	add(Table{Name: "dirty_keys", PK: []string{"key"}, Columns: []Column{
		notNull("key", "text"), notNull("seq", "bigint"), notNull("since", "timestamptz"),
		{Name: "failures", Type: "integer NOT NULL DEFAULT 0"}, col("last_error", "text"), col("failed_at", "timestamptz"),
		// render: a run renders and commits even with no new file (another key changed what its
		// rows show, or a detail object it waits for may have landed: §3.7).
		{Name: "render", Type: "boolean NOT NULL DEFAULT false"},
	}, Indexes: []string{"(since)"}})
	add(Table{Name: "workers", PK: []string{"worker"}, Columns: []Column{notNull("worker", "text"), notNull("seen", "timestamptz")}})
	add(Table{Name: "tail", Columns: []Column{col("last_name", "text"), col("grace", "text[]"), col("checked_at", "timestamptz")},
		Seed: "INSERT INTO tail (last_name, grace) VALUES ('', '{}')"}) // its one row; never checked yet
	add(Table{Name: "ingest_pods", PK: []string{"incarnation"}, Columns: []Column{
		notNull("incarnation", "text"), col("last_ns", "bigint"), col("at", "timestamptz"),
	}})

	// §3.3 Session tables: rendered.
	rendered := func(name string, row any, pk []string, extra []Column, idx ...string) {
		add(Table{Name: name, Kind: Rendered, Row: typ(row), Columns: append(rowColumns(typ(row), rowOpts{}), extra...),
			PK: pk, Indexes: idx})
	}
	rendered("session_rows", process.SessionRow{}, []string{"session_id"},
		[]Column{col("own_title", "text"), col("prompt_last_ns", "bigint"), col("key_day", "text")}, "(key_day)")
	rendered("prompt_rows", process.PromptRow{}, []string{"session_id", "seq"}, nil)
	rendered("prompt_steps", process.PromptStep{}, []string{"session_id", "seq", "step"}, nil)
	rendered("session_marks", process.SessionMark{}, []string{"session_id", "seq"}, nil, "(session_id) WHERE kind = 'missed'")
	rendered("event_rows", process.EventRow{}, []string{"session_id", "seq"}, []Column{col("batch", "text")},
		"(session_id, name, ts_ns) WHERE name IN ('tengu_tool_search_mode_decision', 'tengu_skills_sync_success')")
	rendered("presence_segments", process.PresenceSegment{}, []string{"session_id", "seq"}, nil)
	rendered("session_tools", process.SessionTool{}, []string{"session_id", "kind", "server", "tool"}, nil)
	rendered("call_tool_uses", process.CallToolUse{}, []string{"session_id", "ts_ns", "kind", "server", "tool"}, nil)
	// The Requests tab's rows are the API's flat columns: the request row's aren't prefixed.
	add(Table{Name: "session_request_rows", Kind: Rendered, Row: typ(process.SessionRequestRow{}),
		Columns: rowColumns(typ(process.SessionRequestRow{}), rowOpts{inline: map[string]bool{"row": true}}),
		PK:      []string{"scope_session_id", "seq"}})

	// §3.4 Stored inputs: the state's own rows, keyed by (key, id).
	stored := func(name, stateTable string, opts rowOpts, idx ...string) Table {
		row := reflect.TypeOf(state.TableTypes[stateTable])
		if row == nil {
			panic("pg: no state table " + stateTable)
		}
		cols := append([]Column{notNull("key", "text"), notNull("id", "bytea")}, rowColumns(row, opts)...)
		return Table{Name: name, Kind: Stored, State: stateTable, Row: row, Columns: cols, PK: []string{"key", "id"}, Indexes: idx}
	}
	// A dimension table's index is its account_id, else its person_id (§3.4).
	dim := func(stateTable string, idx ...string) {
		t := stored(stateTable, stateTable, rowOpts{})
		if _, ok := t.Column("account_id"); ok {
			t.Indexes = append([]string{"(account_id)"}, t.Indexes...)
		} else if _, ok := t.Column("person_id"); ok {
			t.Indexes = append([]string{"(person_id)"}, t.Indexes...)
		}
		t.Indexes = append(t.Indexes, idx...)
		add(t)
	}
	dim(state.TableAccounts, "(lower(email))")
	dim(state.TableSessionAccounts, "(session_id)")
	dim(state.TableSessionVisible)
	dim(state.TableSessionPerson)
	dim(state.TablePersonDevices)
	dim(state.TableSessionCredential, "(session_id)")
	dim(state.TableAccountEvents)
	dim(state.TableAccountDevices)
	dim(state.TableCodexPlans)
	add(stored("request_txs", state.TableRequestTxs, rowOpts{}, "(key_hash, row_ts_ns)", "(key_hash, prompt_id)", "(call_id)"))
	add(stored("prompt_index", state.TablePromptIndex, rowOpts{}, "(session_id, prompt_id)"))
	add(stored("codex_turns", state.TableCodexTurns, rowOpts{}, "(session_id, prompt_id)"))
	add(stored("session_last", state.TableSessionLast, rowOpts{}, "(session_id)"))
	add(stored("request_reports", state.TableRequestReports, rowOpts{}, "(req_id)", "(client_req_id)"))
	add(stored("request_missed", state.TableRequestMissed, rowOpts{}, "(session_id)", "(row_request_id)"))
	add(stored("session_call_accounts", state.TableSessionCallAccounts, rowOpts{}, "(account_id)", "(session_id)"))
	add(stored("session_edges", state.TableSessionEdges, rowOpts{}, "(parent_session_id)"))
	add(stored("session_title_calls", state.TableSessionTitleCalls, rowOpts{}, "(account_id, ts_ns)"))
	add(stored("requests", state.TableFactRequests, rowOpts{}, "(call_id)"))
	// The analytics events (state.TableFactEvents) aren't stored: nothing reads them from Postgres
	// (the drawer is precomputed, /events/{id} reads drawer_events, a batch's other keys come from
	// batch_parts), and they were the largest table (docs/PIPELINE.md §3.4).
	add(Table{Name: "batch_parts", Kind: Fold, PK: []string{"batch_id", "key"}, Columns: []Column{
		notNull("batch_id", "text"), notNull("key", "text"), col("n", "bigint"), col("min_name", "text"),
		col("first_ns", "bigint"), col("first_session", "text"),
	}, Indexes: []string{"(key)"}})

	// §3.5 Directory folds. session_call_days.day is _ACTIVITY's day number, floor(t_ns / 1e9 /
	// 86400) in float64; agent_call_hours.hour is the UTC hour's start, Unix ns.
	add(Table{Name: "session_call_days", Kind: Fold, PK: []string{"session_id", "day"}, Columns: []Column{
		notNull("session_id", "text"), notNull("day", "bigint"), col("n", "bigint"), col("first_ns", "bigint"), col("last_ns", "bigint"),
	}})
	add(Table{Name: "agent_calls", Kind: Fold, PK: []string{"key", "call_id"}, Columns: []Column{
		notNull("key", "text"), notNull("call_id", "text"), col("ts_ns", "bigint"),
	}, Indexes: []string{"(ts_ns)"}})
	add(Table{Name: "agent_call_hours", Kind: Fold, PK: []string{"hour", "key"}, Columns: []Column{
		notNull("hour", "bigint"), notNull("key", "text"), col("n", "bigint"),
	}})

	// §3.7 The request drawer: each key's finished drawer rows, read by primary key. A row's
	// stepCount is its (step_session, step_prompt)'s drawer_step_counts row, filled in at read.
	add(Table{Name: "drawer_requests", Kind: Fold, PK: []string{"request_id", "key"}, Columns: []Column{
		notNull("request_id", "text"), notNull("key", "text"), col("data", "text"), col("details", "text"),
		col("step_session", "text"), col("step_prompt", "text"),
	}, Indexes: []string{"(key)"}, LZ4: []string{"data", "details"}})
	add(Table{Name: "drawer_step_counts", Kind: Fold, PK: []string{"session_id", "prompt_id", "key"}, Columns: []Column{
		notNull("session_id", "text"), notNull("prompt_id", "text"), notNull("key", "text"), col("n", "bigint"),
	}, Indexes: []string{"(key)"}})
	add(Table{Name: "drawer_events", Kind: Fold, PK: []string{"event_id", "key"}, Columns: []Column{
		notNull("event_id", "text"), notNull("key", "text"), col("data", "text"), col("correlation_id", "text"),
		col("event_key", "text"),
	}, Indexes: []string{"(key)"}})

	for i := range ts {
		checkTable(&ts[i])
	}
	return ts
}

// checkTable panics on a table that can't be right: a repeated column, or a
// key or index naming a column it doesn't have.
func checkTable(t *Table) {
	seen := map[string]bool{}
	for _, c := range t.Columns {
		if seen[c.Name] {
			panic(fmt.Sprintf("pg: %s has two %s columns", t.Name, c.Name))
		}
		seen[c.Name] = true
	}
	for _, k := range t.PK {
		if !seen[k] {
			panic(fmt.Sprintf("pg: %s's primary key names %s, which it doesn't have", t.Name, k))
		}
	}
}

// rowColumns is a row type's columns, by its parquet tags: a nested struct's
// columns are prefixed with its name (row_<column>, plan_v), an embedded one's
// aren't, and a "-" field isn't stored.
func rowColumns(t reflect.Type, o rowOpts) []Column {
	var out []Column
	var walk func(t reflect.Type, prefix string, path []int)
	walk = func(t reflect.Type, prefix string, path []int) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("parquet"), ",")
			at := append(append([]int(nil), path...), i)
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch {
			case name == "-":
				continue
			case f.Anonymous && ft.Kind() == reflect.Struct:
				walk(ft, prefix, at)
				continue
			case name == "":
				name = strings.ToLower(f.Name)
			}
			if o.omit[prefix+name] {
				continue
			}
			if ft.Kind() == reflect.Struct {
				p := prefix + name + "_"
				if o.inline[name] {
					p = prefix
				}
				walk(ft, p, at)
				continue
			}
			out = append(out, Column{Name: prefix + name, Type: sqlType(ft), Field: at,
				Optional: strings.Contains(","+opts+",", ",optional,")})
		}
	}
	walk(t, "", nil)
	return out
}

// sqlType is a Go field type's column type.
func sqlType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "text"
	case reflect.Int64, reflect.Int, reflect.Int32, reflect.Uint64, reflect.Uint32:
		return "bigint"
	case reflect.Float64, reflect.Float32:
		return "double precision"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice:
		switch t.Elem().Kind() {
		case reflect.String:
			return "text[]"
		case reflect.Int64, reflect.Int:
			return "bigint[]"
		}
	}
	panic("pg: no column type for " + t.String())
}
