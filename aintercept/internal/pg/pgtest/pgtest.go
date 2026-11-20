// Package pgtest gives a test its own fresh Postgres database (docs/PIPELINE.md,
// Testing): on the server AINTERCEPT_TEST_PG names (an admin DSN: CI's service
// container, or the container `make check` starts with scripts/test-pg.sh).
// Without it the test is skipped, unless AINTERCEPT_TEST_PG_REQUIRED=1 (CI),
// which fails it instead.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
)

// AdminDSN is the test server's admin DSN, or skips (or fails) t without one.
func AdminDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("AINTERCEPT_TEST_PG")
	if dsn == "" {
		if os.Getenv("AINTERCEPT_TEST_PG_REQUIRED") == "1" {
			t.Fatal("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
		}
		t.Skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
	}
	return dsn
}

// Fresh creates an empty database with the built-in C.UTF-8 locale (§3.1),
// dropped when t ends, and returns its admin DSN.
func Fresh(t testing.TB) string {
	t.Helper()
	admin := AdminDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connecting to the test Postgres: %v", err)
	}
	defer conn.Close(ctx)
	b := make([]byte, 6)
	rand.Read(b)
	name := "t_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name+
		" TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8'"); err != nil {
		t.Fatalf("creating a test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if c, err := pgx.Connect(ctx, admin); err == nil {
			c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(ctx)
		}
	})
	return WithDatabase(t, admin, name)
}

// WithDatabase is dsn (a URL) naming another database.
func WithDatabase(t testing.TB, dsn, db string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("AINTERCEPT_TEST_PG must be a postgres:// URL: %v", err)
	}
	u.Path = "/" + db
	return u.String()
}

// WithUser is dsn (a URL) logging in as user with password.
func WithUser(t testing.TB, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("AINTERCEPT_TEST_PG must be a postgres:// URL: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

// Chain is the workers' migrations and then a test's, sqls, numbered after
// them (NNNN_test_<n>.sql, each with goose's `-- +goose Up` first): a chain
// for pg.Migrate.
func Chain(sqls ...string) fs.FS {
	m := fstest.MapFS{}
	names, _ := fs.Glob(pg.Migrations, "*.sql")
	for _, n := range names {
		b, _ := fs.ReadFile(pg.Migrations, n)
		m[n] = &fstest.MapFile{Data: b}
	}
	for i, sql := range sqls {
		m[fmt.Sprintf("%04d_test_%d.sql", pg.Latest+int64(i)+1, i+1)] = &fstest.MapFile{Data: []byte("-- +goose Up\n" + sql)}
	}
	return m
}
