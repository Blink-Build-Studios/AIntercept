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

// Schema is the schema (§3.1): one embedded file, generated from Tables.
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
	// migrations: a schema change means a fresh database.
	ErrSchemaMismatch = errors.New("pg: the database's schema isn't this build's (a schema change needs a fresh database)")
	// ErrNoSchema is a database the schema was never applied to.
	ErrNoSchema = errors.New("pg: the database has no schema")
	// ErrNotEmpty is a database with tables but no schema_version: not one
	// Apply would apply the schema to.
	ErrNotEmpty = errors.New("pg: the database has tables but no schema_version")
)

// schemaLock is the advisory lock Apply holds, so concurrent workers apply the
// schema once.
const schemaLock int64 = 0x61696e7465720001

// Querier is a connection, a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Apply applies the schema to an empty database and records its hash, or
// checks the hash already recorded: the workers call it at start and refuse to
// start on its error (ErrSchemaMismatch, ErrNotEmpty).
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", schemaLock); err != nil {
		return err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", schemaLock); err != nil {
			conn.Conn().Close(context.WithoutCancel(ctx)) // closing the session frees the lock
		}
	}()
	if err := Check(ctx, conn); !errors.Is(err, ErrNoSchema) {
		return err
	}
	var tables int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()").Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		return ErrNotEmpty
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, Schema); err != nil {
			return fmt.Errorf("pg: applying the schema: %w", err)
		}
		_, err := tx.Exec(ctx, "INSERT INTO schema_version (hash) VALUES ($1)", SchemaHash)
		return err
	})
}

// Check fails unless the database's schema is this one (ErrNoSchema,
// ErrSchemaMismatch): the API calls it at start and refuses to start on its
// error.
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
