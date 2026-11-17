package pg

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is the schema (§3): one embedded, hand-written file.
//
//go:embed schema.sql
var Schema string

// SchemaHash is Schema's sha256, hex: what schema_version records.
var SchemaHash = func() string {
	sum := sha256.Sum256([]byte(Schema))
	return hex.EncodeToString(sum[:])
}()

var (
	// ErrSchemaMismatch is a database whose schema isn't this one. There are no
	// migrations: a schema change means a fresh database, which ApplyOrReset
	// makes.
	ErrSchemaMismatch = errors.New("pg: the database's schema isn't this build's")
	// ErrNoSchema is a database the schema was never applied to.
	ErrNoSchema = errors.New("pg: the database has no schema")
	// ErrNotEmpty is a database with tables but no schema_version: not one
	// Apply would apply the schema to, or ApplyOrReset reset.
	ErrNotEmpty = errors.New("pg: the database has tables but no schema_version")
)

// schemaLock is the advisory lock Apply holds, so concurrent workers apply the
// schema (or reset the database) once.
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

// Apply applies the schema to an empty database and records its hash, or
// checks the hash already recorded: it fails on another build's schema
// (ErrSchemaMismatch) or a database that isn't one (ErrNotEmpty).
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := apply(ctx, pool, false)
	return err
}

// ApplyOrReset is Apply, except that a database holding another build's
// schema is reset first: everything in it is dropped and this build's schema
// applied, in one transaction, and reset is true. The workers call it at start
// (WORKER_SCHEMA_RESET): every fact reloads from the raw files, since
// ingest_files starts empty. A database with tables but no schema_version is
// still refused (ErrNotEmpty).
func ApplyOrReset(ctx context.Context, pool *pgxpool.Pool) (reset bool, err error) {
	return apply(ctx, pool, true)
}

func apply(ctx context.Context, pool *pgxpool.Pool, resetting bool) (reset bool, err error) {
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Workers starting together wait for the first; a reset waits out the
		// API's reads (30 s at most) for its locks.
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '1min'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLock); err != nil {
			return err
		}
		switch err := Check(ctx, tx); {
		case err == nil:
			return nil
		case errors.Is(err, ErrSchemaMismatch) && resetting:
			if _, err := tx.Exec(ctx, resetSQL); err != nil {
				return fmt.Errorf("pg: resetting the database: %w", err)
			}
			reset = true
		case errors.Is(err, ErrNoSchema):
			var tables int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()").Scan(&tables); err != nil {
				return err
			}
			if tables > 0 {
				return ErrNotEmpty
			}
		default:
			return err
		}
		if _, err := tx.Exec(ctx, Schema); err != nil {
			return fmt.Errorf("pg: applying the schema: %w", err)
		}
		_, err := tx.Exec(ctx, "INSERT INTO schema_version (hash) VALUES ($1)", SchemaHash)
		return err
	})
	return reset && err == nil, err
}

// Check fails unless the database's schema is this one (ErrNoSchema,
// ErrSchemaMismatch). The API makes the same check (reader.check_schema).
func Check(ctx context.Context, q Querier) error {
	var reg *string
	if err := q.QueryRow(ctx, "SELECT to_regclass('schema_version')::text").Scan(&reg); err != nil {
		return err
	}
	if reg == nil {
		return ErrNoSchema
	}
	var hashes []string
	if err := q.QueryRow(ctx, "SELECT coalesce(array_agg(hash), '{}') FROM schema_version").Scan(&hashes); err != nil {
		return err
	}
	if len(hashes) != 1 || hashes[0] != SchemaHash {
		return fmt.Errorf("%w: it has %v, this build's is %s", ErrSchemaMismatch, hashes, SchemaHash)
	}
	return nil
}
