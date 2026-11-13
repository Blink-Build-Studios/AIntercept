// Package pg is the workers' Postgres (docs/PIPELINE.md v7): the connection
// pool with the spec's settings, the schema (schema.sql, generated from
// Tables) and its hash, and the two logins. Ingest never imports it.
package pg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Keepalives is how a connection notices a dead peer (§6): TCP keepalives
// after Idle, every Interval, Count times, and the kernel's user timeout
// (Linux), so a dropped connection is noticed in about 30 s either way. Every
// DSN carries them (keepalives_idle, keepalives_interval, keepalives_count,
// tcp_user_timeout: libpq's names, which the API's driver reads as is); pgx
// doesn't know them, so ParseConfig takes them out and applies them itself.
type Keepalives struct {
	Idle, Interval time.Duration
	Count          int
	UserTimeout    time.Duration
}

// DefaultKeepalives are the spec's, used for whatever a DSN leaves out.
var DefaultKeepalives = Keepalives{Idle: 10 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second}

// ParseConfig is a pool's config for dsn (a URL or key=value DSN), with its
// keepalives applied to every connection, and at most maxConns connections.
func ParseConfig(dsn string, maxConns int32) (*pgxpool.Config, Keepalives, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, Keepalives{}, err
	}
	ka := DefaultKeepalives
	params := cfg.ConnConfig.RuntimeParams
	for name, set := range map[string]func(int){
		"keepalives_idle":     func(n int) { ka.Idle = time.Duration(n) * time.Second },
		"keepalives_interval": func(n int) { ka.Interval = time.Duration(n) * time.Second },
		"keepalives_count":    func(n int) { ka.Count = n },
		"tcp_user_timeout":    func(n int) { ka.UserTimeout = time.Duration(n) * time.Millisecond },
	} {
		v, ok := params[name]
		if !ok {
			continue
		}
		delete(params, name) // not a server setting: sent as one, the server would refuse the connection
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, Keepalives{}, fmt.Errorf("pg: %s=%q isn't a count", name, v)
		}
		set(n)
	}
	delete(params, "keepalives") // libpq's switch: always on here
	d := &net.Dialer{
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: ka.Idle, Interval: ka.Interval, Count: ka.Count},
		Control:         userTimeout(ka.UserTimeout),
	}
	cfg.ConnConfig.DialFunc = d.DialContext
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	return cfg, ka, nil
}

// Open is a pool for dsn (ParseConfig), with a connection checked.
func Open(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, _, err := ParseConfig(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
