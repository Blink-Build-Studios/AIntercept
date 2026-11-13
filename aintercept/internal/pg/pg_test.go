package pg

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite schema.sql from Tables")

// TestSchemaFile holds schema.sql to Tables: the embedded file is exactly what
// the Go row types generate, so a column added to a row type can't be missing
// from Postgres. `go test ./internal/pg -run TestSchemaFile -update` rewrites it.
func TestSchemaFile(t *testing.T) {
	want := DDL()
	if *update {
		if err := os.WriteFile("schema.sql", []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if Schema != want {
		t.Fatal("schema.sql is stale: go test ./internal/pg -run TestSchemaFile -update")
	}
}

func TestSchemaShape(t *testing.T) {
	cases := []struct {
		table string
		cols  []string // some of its columns, in order
		pk    string
	}{
		// A stored input: (key, id bytea), then the state row's columns; a pick flattened.
		{"dim_account_events", []string{"key", "id", "account_id", "org", "org_ns", "plan_v", "plan_ts", "plan_ok"}, "key, id"},
		// A nested request row: row_<column>.
		{"request_txs", []string{"key", "id", "call_id", "correlation_id", "key_hash", "own_session", "prompt_id"}, "key, id"},
		{"request_missed", []string{"session_id", "event_key", "req_id", "client_req_id", "row_person_id", "row_request_id"}, "key, id"},
		// The Requests tab's rows: the API's flat columns.
		{"session_request_rows", []string{"scope_session_id", "seq", "person_id", "request_id", "ts_ns"}, "scope_session_id, seq"},
		// Rendered rows keep today's columns, and their extras come last.
		{"session_rows", []string{"session_id", "start_ns", "last_ns", "product", "agent", "client"}, "session_id"},
		{"event_rows", []string{"session_id", "seq", "event_id"}, "session_id, seq"},
		{"analytics", []string{"key", "id", "event_key", "correlation_id"}, "key, id"},
		{"call_tool_uses", []string{"session_id", "ts_ns", "kind", "server", "tool"}, "session_id, ts_ns, kind, server, tool"},
	}
	for _, c := range cases {
		tb := TableNamed(c.table)
		if tb == nil {
			t.Fatalf("no table %s", c.table)
		}
		var names []string
		for _, col := range tb.Columns {
			names = append(names, col.Name)
		}
		joined := strings.Join(names, ",")
		if !strings.Contains(joined, strings.Join(c.cols, ",")) {
			t.Errorf("%s: columns %s, want them to include %v in order", c.table, joined, c.cols)
		}
		if got := strings.Join(tb.PK, ", "); got != c.pk {
			t.Errorf("%s: primary key (%s), want (%s)", c.table, got, c.pk)
		}
	}
	for _, x := range []struct{ table, col string }{
		{"analytics", "env_json"},      // not stored: no API code reads it
		{"session_rows", "title"},      // parquet "-"
		{"call_tool_uses", "call_id"},  // the state's, not the API's
		{"fact_pending", "start_ns"},   // not stored: the drawer 404s for a call in flight
		{"batch_members", "event_key"}, // batch parts instead
	} {
		if tb := TableNamed(x.table); tb != nil {
			if _, ok := tb.Column(x.col); ok {
				t.Errorf("%s has %s", x.table, x.col)
			}
		}
	}
	extras := map[string][]string{
		"session_rows": {"own_title", "prompt_last_ns", "key_day"},
		"event_rows":   {"batch"},
	}
	for table, cols := range extras {
		tb := TableNamed(table)
		n := len(tb.Columns)
		for i, c := range cols {
			if got := tb.Columns[n-len(cols)+i]; got.Name != c || got.Field != nil {
				t.Errorf("%s: extra %d is %s (field %v), want %s", table, i, got.Name, got.Field, c)
			}
		}
	}
}

func TestParseConfigAppliesKeepalives(t *testing.T) {
	cases := []struct {
		name, dsn string
		want      Keepalives
	}{
		{"the spec's, in a URL", "postgres://u@h:5432/db?keepalives_idle=10&keepalives_interval=5&keepalives_count=3&tcp_user_timeout=30000&sslmode=disable",
			Keepalives{10 * time.Second, 5 * time.Second, 3, 30 * time.Second}},
		{"key=value", "host=h user=u dbname=db keepalives=1 keepalives_idle=7 keepalives_interval=2 keepalives_count=4 tcp_user_timeout=9000 sslmode=disable",
			Keepalives{7 * time.Second, 2 * time.Second, 4, 9 * time.Second}},
		{"none: the spec's", "postgres://u@h/db?sslmode=disable", DefaultKeepalives},
	}
	for _, c := range cases {
		cfg, ka, err := ParseConfig(c.dsn, 3)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ka != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, ka, c.want)
		}
		for _, p := range []string{"keepalives", "keepalives_idle", "keepalives_interval", "keepalives_count", "tcp_user_timeout"} {
			if _, ok := cfg.ConnConfig.RuntimeParams[p]; ok {
				t.Errorf("%s: %s would be sent to the server as a setting", c.name, p)
			}
		}
		if cfg.ConnConfig.DialFunc == nil || cfg.MaxConns != 3 {
			t.Errorf("%s: dial func %v, max conns %d", c.name, cfg.ConnConfig.DialFunc != nil, cfg.MaxConns)
		}
	}
	if _, _, err := ParseConfig("postgres://u@h/db?keepalives_idle=soon", 1); err == nil {
		t.Error("a bad keepalives_idle was accepted")
	}
}

func TestLoginsSQLFillsPasswords(t *testing.T) {
	sql := LoginsSQL("w'1", "a2")
	if !strings.Contains(sql, "aintercept_worker LOGIN PASSWORD 'w''1'") || !strings.Contains(sql, "aintercept_api LOGIN PASSWORD 'a2'") {
		t.Fatalf("passwords not filled in:\n%s", sql)
	}
	if strings.Contains(sql, ":'") {
		t.Fatal("a psql variable is left")
	}
}
