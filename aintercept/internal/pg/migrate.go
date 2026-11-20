package pg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

// The migrations (docs/PIPELINE.md §3.4): goose SQL files, forward only,
// numbered from 0001 and embedded in the binary. goose applies them, each in
// its own transaction, and records them in its table (goose_db_version), under
// its Postgres session lock. Ours is the rest: what a database from before
// migrations is, the directives below, and refusing a database newer than the
// build. 0001 is v0.6.2's schema.sql byte for byte below its `-- +goose Up`
// line, so a database v0.6.2 made (that file's hash in schema_version) is
// version 1 as it stands.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations is this build's chain, as goose reads it, and Latest its newest
// version.
var (
	Migrations = func() fs.FS {
		sub, err := fs.Sub(migrationFiles, "migrations")
		if err != nil {
			panic(err)
		}
		return sub
	}()
	Latest = func() int64 {
		ms, err := ParseMigrations(Migrations)
		if err != nil {
			panic(err)
		}
		return ms[len(ms)-1].Version
	}()
)

// Migration is what our code reads of a migration file, NNNN_<name>.sql: its
// leading comment lines can say what it needs besides its SQL.
//
//	-- reload: <table>, <table>   empty those tables and ingest_files before it runs: the workers reload every raw file
//	-- reset                      empty the database first: goose applies the whole chain, and everything reloads
type Migration struct {
	Version int64
	Name    string // the file's
	SQL     string
	Reload  []string
	Reset   bool
}

var (
	migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
	tableName     = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// ParseMigration is the migration in the file name holding sql.
func ParseMigration(name, sql string) (Migration, error) {
	f := migrationName.FindStringSubmatch(name)
	if f == nil {
		return Migration{}, fmt.Errorf("pg: migration %s isn't named NNNN_<name>.sql", name)
	}
	v, _ := strconv.ParseInt(f[1], 10, 64)
	if v == 0 {
		return Migration{}, fmt.Errorf("pg: migration %s: the chain starts at 0001", name)
	}
	m := Migration{Version: v, Name: name, SQL: sql}
	for _, line := range strings.Split(sql, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		h, ok := strings.CutPrefix(line, "--")
		if !ok {
			break // the header is the leading comment lines (goose's annotation among them)
		}
		switch h = strings.TrimSpace(h); {
		case h == "reset":
			m.Reset = true
		case strings.HasPrefix(h, "reload:"):
			for _, t := range strings.Split(strings.TrimPrefix(h, "reload:"), ",") {
				if t = strings.TrimSpace(t); !tableName.MatchString(t) {
					return Migration{}, fmt.Errorf("pg: migration %s: -- reload: %q isn't a table's name", name, t)
				}
				m.Reload = append(m.Reload, t)
			}
		}
	}
	return m, nil
}

// ParseMigrations is the chain in fsys's *.sql files, in order.
func ParseMigrations(fsys fs.FS) ([]Migration, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		m, err := ParseMigration(name, string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errors.New("pg: no migrations")
	}
	return out, nil
}

// Applied is what Migrate did: the database's version before (0: empty, or
// one the chain can't reach) and after; whether it was v0.6.2's, from before
// migrations (recorded as version 1 in place); whether it emptied the database
// (one the chain can't reach, or a -- reset migration); and the tables it
// emptied for a reload.
type Applied struct {
	From, To int64
	V062     bool
	Reset    bool
	Reload   []string
}

// Migrate brings the database to the newest version of the chain in fsys. An
// empty database gets the whole chain; one from before migrations at v0.6.2's
// hash is recorded as version 1 in place. One the chain can't reach (from
// before migrations, at another hash) is reset with resetting
// (WORKER_SCHEMA_RESET), else refused (ErrSchemaMismatch). A database newer
// than the chain (a rollback) is always refused (ErrNewer), and one with tables
// but no versions too (ErrNotEmpty).
//
// Our part runs first, in one transaction, then goose applies what's pending.
// Both hold the same session lock (schemaLock): a worker that looks in between
// finds what the first one's transaction left, so does the same, or nothing.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, resetting bool) (Applied, error) {
	ms, err := ParseMigrations(fsys)
	if err != nil {
		return Applied{}, err
	}
	db, store, locker, err := gooseDB(pool)
	if err != nil {
		return Applied{}, err
	}
	defer db.Close()
	a, err := prepare(ctx, db, store, locker, ms, resetting)
	if err != nil {
		return a, err
	}
	if err := up(ctx, db, locker, fsys); err != nil {
		return a, err
	}
	if err := markV062(ctx, db, store, locker, ms[0]); err != nil {
		return a, err
	}
	a.To, err = store.GetLatestVersion(ctx, db)
	return a, err
}

// markV062 records v0.6.2's hash in the schema_version of a database at
// version 1 (v0.6.2's schema) that this build made, fresh or reset, as v0.6.2
// did. A rollback to v0.6.2 then finds its own schema and runs, where an empty
// schema_version would have its workers reset the database. Past version 1, a
// migration drops schema_version (§3.4), and v0.6.2 refuses the database.
func markV062(ctx context.Context, db *sql.DB, store database.Store, locker lock.SessionLocker, first Migration) error {
	return withLock(ctx, db, locker, func(ctx context.Context, tx *sql.Tx) error {
		v, err := store.GetLatestVersion(ctx, tx)
		if err != nil || v != 1 {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO schema_version (hash) SELECT $1 WHERE NOT EXISTS (SELECT FROM schema_version)",
			v062Hash(first))
		return err
	})
}

// gooseDB is a database/sql handle for goose, on connections of its own: the
// pool's settings, but no statement timeout (a migration takes as long as it
// takes, not the worker login's minute) and a minute's wait for a lock (a
// migration waits out the API's reads, 30 s at most). With goose's table and
// its session locker, which polls each second for up to ten minutes (a worker
// waits for another's migrations).
func gooseDB(pool *pgxpool.Pool) (*sql.DB, database.Store, lock.SessionLocker, error) {
	cc := pool.Config().ConnConfig
	cc.RuntimeParams["statement_timeout"] = "0"
	cc.RuntimeParams["lock_timeout"] = "1min"
	store, err := database.NewStore(goose.DialectPostgres, goose.DefaultTablename)
	if err != nil {
		return nil, nil, nil, err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(schemaLock), lock.WithLockTimeout(1, 600))
	if err != nil {
		return nil, nil, nil, err
	}
	return stdlib.OpenDB(*cc), store, locker, nil
}

func up(ctx context.Context, db *sql.DB, locker lock.SessionLocker, fsys fs.FS) error {
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("pg: migrating: %w", err)
	}
	return nil
}

// prepare is our part of Migrate, in one transaction under the session lock.
func prepare(ctx context.Context, db *sql.DB, store database.Store, locker lock.SessionLocker, ms []Migration, resetting bool) (Applied, error) {
	var a Applied
	err := withLock(ctx, db, locker, func(ctx context.Context, tx *sql.Tx) error {
		v, legacy, err := version(ctx, tx, store, ms[0])
		a.From = v
		latest := ms[len(ms)-1].Version
		var pending []Migration
		for _, m := range ms {
			if m.Version > v {
				pending = append(pending, m)
			}
		}
		switch {
		case errors.Is(err, ErrSchemaMismatch) && resetting:
			a.Reset = true
			return resetTx(ctx, tx)
		case err != nil:
			return err
		case v > latest:
			return fmt.Errorf("%w: the database is at schema version %d and this build's newest is %d; "+
				"an older build can't run on it (run one with migration %04d)", ErrNewer, v, latest, v)
		case v == 0:
			return nil // empty: goose applies the chain
		}
		if legacy {
			// v0.6.2's schema is version 1, in place: goose's table, recording
			// 0 (as goose starts it) and 1.
			a.V062 = true
			if err := store.CreateVersionTable(ctx, tx); err != nil {
				return err
			}
			for _, n := range []int64{0, 1} {
				if err := store.Insert(ctx, tx, database.InsertRequest{Version: n}); err != nil {
					return err
				}
			}
		}
		if slices.ContainsFunc(pending, func(m Migration) bool { return m.Reset }) {
			a.Reset = true
			return resetTx(ctx, tx)
		}
		for _, m := range pending {
			a.Reload = append(a.Reload, m.Reload...)
		}
		if len(a.Reload) > 0 {
			return empty(ctx, tx, a.Reload)
		}
		return nil
	})
	return a, err
}

// version is the database's schema version: goose's, or for a database from
// before migrations 1 (legacy) when its schema_version holds the hash of v0.6.2's
// schema (first's, below its goose annotation); 0 for an empty one.
func version(ctx context.Context, tx *sql.Tx, store database.Store, first Migration) (v int64, legacy bool, err error) {
	var gooseTable, hashTable bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL, to_regclass('schema_version') IS NOT NULL`,
		store.Tablename()).Scan(&gooseTable, &hashTable); err != nil {
		return 0, false, err
	}
	switch {
	case gooseTable:
		v, err := store.GetLatestVersion(ctx, tx)
		return v, false, err
	case hashTable:
		var n int
		var hash string
		if err := tx.QueryRowContext(ctx, "SELECT count(*), coalesce(min(hash), '') FROM schema_version").Scan(&n, &hash); err != nil {
			return 0, false, err
		}
		if n == 1 && hash == v062Hash(first) {
			return 1, true, nil
		}
		return 0, false, fmt.Errorf("%w: it's from before migrations (schema hash %q), and they start at v0.6.2's", ErrSchemaMismatch, hash)
	}
	var tables int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()").Scan(&tables); err != nil {
		return 0, false, err
	}
	if tables > 0 {
		return 0, false, ErrNotEmpty
	}
	return 0, false, nil
}

// v062Hash is the hash v0.6.2 recorded in schema_version: its schema.sql's
// sha256, which is migration 1 below its first line (`-- +goose Up`).
func v062Hash(first Migration) string {
	_, body, _ := strings.Cut(first.SQL, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func resetTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, resetSQL); err != nil {
		return fmt.Errorf("pg: resetting the database: %w", err)
	}
	return nil
}

// empty truncates the tables (those that exist yet: one a migration creates
// starts empty) and ingest_files, so the workers list and load every raw file
// again (ingest.List lists from the start when ingest_files is empty).
func empty(ctx context.Context, tx *sql.Tx, tables []string) error {
	ids := []string{"ingest_files"}
	for _, t := range tables {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", t).Scan(&exists); err != nil {
			return err
		}
		if exists {
			ids = append(ids, pgx.Identifier{t}.Sanitize())
		}
	}
	if _, err := tx.ExecContext(ctx, "TRUNCATE "+strings.Join(ids, ", ")); err != nil {
		return fmt.Errorf("pg: emptying %v for a reload: %w", tables, err)
	}
	return nil
}

// Reset empties the database and applies this build's chain: `aintercept pg
// reset`. The workers then reload every raw file.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	ms, err := ParseMigrations(Migrations)
	if err != nil {
		return err
	}
	db, store, locker, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := withLock(ctx, db, locker, resetTx); err != nil {
		return err
	}
	if err := up(ctx, db, locker, Migrations); err != nil {
		return err
	}
	return markV062(ctx, db, store, locker, ms[0])
}

// withLock runs f in one transaction under the session lock.
func withLock(ctx context.Context, db *sql.DB, locker lock.SessionLocker, f func(context.Context, *sql.Tx) error) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := locker.SessionLock(ctx, conn); err != nil {
		return fmt.Errorf("pg: waiting for another worker's migrations: %w", err)
	}
	defer func() { err = errors.Join(err, locker.SessionUnlock(context.WithoutCancel(ctx), conn)) }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
