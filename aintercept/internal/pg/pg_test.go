package pg

import (
	"strings"
	"testing"
	"time"
)

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

// The analytics events (state's fact_events) aren't stored: no API statement
// and no worker read takes them from Postgres since the drawer is
// precomputed (/events/{id} reads drawer_events, a batch's other keys come
// from batch_parts), so writing them only cost the database its largest
// table (docs/PIPELINE.md §3.4).
