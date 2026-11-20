package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The schema (§3) is the migrations' (migrate.go); schema.sql is the schema
// they build, to read, which the tests hold equal to them.

var (
	// ErrSchemaMismatch is a database the migrations can't reach: one from
	// before them at another hash than v0.6.2's (Migrate resets it with
	// resetting). Check also says it of a database at an older version.
	ErrSchemaMismatch = errors.New("pg: the database's schema isn't this build's")
	// ErrNewer is a database at a version newer than the build's: a rollback.
	// It's never reset.
	ErrNewer = errors.New("pg: the database is newer than this build")
	// ErrNoSchema is a database the schema was never applied to.
	ErrNoSchema = errors.New("pg: the database has no schema")
	// ErrNotEmpty is a database with tables but no versions: not one Migrate
	// would apply the schema to, or reset.
	ErrNotEmpty = errors.New("pg: the database has tables but no schema version")
)

// schemaLock is the session lock Migrate holds (goose's Postgres locker, with
// this id), so concurrent workers migrate (or reset the database) once.
const schemaLock int64 = 0x61696e7465720001

// resetSQL empties the database for this build's schema: a new public schema,
// with the ownership and grants logins.sql gives it (the workers' login owns
// the database and the schema, so it can; the API's reads every table it
// creates). Where there's no API login (the tests' admin) the grants are left
// out.
const resetSQL = `
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'aintercept_api') THEN
    GRANT USAGE ON SCHEMA public TO aintercept_api;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO aintercept_api;
  END IF;
END
$$;`

// Querier is a connection, a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Apply is Migrate with this build's chain, never resetting.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := Migrate(ctx, pool, Migrations, false)
	return err
}

// Check fails unless the database is at this build's version (ErrNoSchema,
// ErrSchemaMismatch, ErrNewer).
func Check(ctx context.Context, q Querier) error {
	var gooseTable, hashTable bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL, to_regclass('schema_version') IS NOT NULL").
		Scan(&gooseTable, &hashTable); err != nil {
		return err
	}
	switch {
	case !gooseTable && hashTable:
		return fmt.Errorf("%w: it's from before migrations", ErrSchemaMismatch)
	case !gooseTable:
		return ErrNoSchema
	}
	var v int64
	if err := q.QueryRow(ctx, "SELECT coalesce(max(version_id), 0) FROM goose_db_version").Scan(&v); err != nil {
		return err
	}
	switch {
	case v > Latest:
		return fmt.Errorf("%w: it's at version %d, this build's is %d", ErrNewer, v, Latest)
	case v < Latest:
		return fmt.Errorf("%w: it's at version %d, this build's is %d", ErrSchemaMismatch, v, Latest)
	}
	return nil
}
