// Package ingest is stage 2 of v9's ingest (docs/PIPELINE.md §2.2): the
// workers list the observers' raw files into ingest_files, and load each one
// into the fact tables in one transaction. Postgres does the coordination: an
// advisory lock makes one lister at a time, FOR UPDATE SKIP LOCKED hands each
// file to one loader, and every write is a keyed upsert, so a file loaded
// twice changes nothing.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/flush"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/refresh"
)

// ListLock is the listing's advisory lock ("aintercept" in ASCII, then a
// number; the rollup worker's singletons are …0010 and up).
const ListLock int64 = 0x61696e7465720020

// Lookback is how far before its cursor a listing starts, for files that
// landed late. Files are named by the hour they're uploaded in, so a listing
// reads whole hour folders from the one this falls in.
const Lookback = 5 * time.Minute

// List lists the raw files under prefix that landed since the last listing
// (its cursor: the newest listed_at) less Lookback, and adds the new ones to
// ingest_files. Only one worker lists at a time: ok is false when another
// holds the lock. It returns how many files were new.
func List(ctx context.Context, db *pgxpool.Pool, store process.Store, prefix string) (n int, ok bool, err error) {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return 0, false, err
	}
	defer conn.Release()
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", ListLock).Scan(&ok); err != nil || !ok {
		return 0, false, err
	}
	defer func() {
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", ListLock); uerr != nil {
			conn.Conn().Close(context.Background()) // a closed session frees its locks
		}
	}()
	var cursor *time.Time
	if err := conn.QueryRow(ctx, "SELECT max(listed_at) FROM ingest_files").Scan(&cursor); err != nil {
		return 0, true, err
	}
	from := "" // the first hour folder to list, YYYY-MM-DD/HH ("": all)
	if cursor != nil {
		from = cursor.Add(-Lookback).UTC().Format("2006-01-02/15")
	}
	keys, err := listFrom(ctx, store, records.RawPrefix(prefix), from)
	if err != nil {
		return 0, true, err
	}
	if len(keys) == 0 {
		return 0, true, nil
	}
	tag, err := conn.Exec(ctx, `INSERT INTO ingest_files (key, listed_at)
		SELECT k, now() FROM unnest($1::text[]) AS k ORDER BY k ON CONFLICT (key) DO NOTHING`, keys)
	if err != nil {
		return 0, true, err
	}
	return int(tag.RowsAffected()), true, nil
}

// listFrom is every raw file in the hour folders root/<date>/<hour>/ from
// `from` (YYYY-MM-DD/HH) on.
func listFrom(ctx context.Context, store process.Store, root, from string) ([]string, error) {
	days, err := store.ListDirs(ctx, root)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, day := range days {
		d := strings.TrimSuffix(strings.TrimPrefix(day, root), "/")
		if from != "" && d < from[:10] {
			continue
		}
		hours, err := store.ListDirs(ctx, day)
		if err != nil {
			return nil, err
		}
		for _, hour := range hours {
			if h := d + "/" + strings.TrimSuffix(strings.TrimPrefix(hour, day), "/"); from != "" && h < from {
				continue
			}
			objs, err := store.List(ctx, hour)
			if err != nil {
				return nil, err
			}
			for _, o := range objs {
				if _, ok := records.RawPod(o.Key); ok && strings.HasSuffix(o.Key, ".ndjson.gz") {
					keys = append(keys, o.Key)
				}
			}
		}
	}
	return keys, nil
}

// LoadOne claims one unloaded file and loads it: its records through the
// flush (docs/PIPELINE.md §4), each completed call's byte range onto its calls
// row, and loaded_at, in one transaction. It returns the file's key, or ""
// when there was none to claim.
func LoadOne(ctx context.Context, db *pgxpool.Pool, store process.Store) (string, error) {
	key, err := Load(ctx, db, store, nil)
	if err != nil {
		return "", err
	}
	return key, nil
}

// Load is LoadOne, passing over the keys in skip (files that failed this
// pass, so one that can't be loaded never holds up the others; it's tried
// again next pass, and holds /lag up meanwhile). On a failure it returns the
// file's key with the error ("" when none was claimed).
func Load(ctx context.Context, db *pgxpool.Pool, store process.Store, skip []string) (string, error) {
	var key string
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT key FROM ingest_files WHERE loaded_at IS NULL AND key <> ALL($1)
			ORDER BY key LIMIT 1 FOR UPDATE SKIP LOCKED`, append([]string{}, skip...)).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		b, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("ingest: %s: %w", key, err)
		}
		return LoadTx(ctx, tx, key, b)
	})
	return key, err
}

// LoadTx writes one raw file's contents in tx (each completed call's row with
// its member's byte range) and marks it loaded.
func LoadTx(ctx context.Context, tx pgx.Tx, key string, b []byte) error {
	recs, ranges, err := records.ReadRawFile(b)
	if err != nil {
		return fmt.Errorf("ingest: %s: %w", key, err)
	}
	pod, _ := records.RawPod(key)
	if err := flush.WriteTx(ctx, tx, pod, recs, key, ranges); err != nil {
		return fmt.Errorf("ingest: %s: %w", key, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingest_files (key, listed_at, loaded_at) VALUES ($1, now(), now())
		ON CONFLICT (key) DO UPDATE SET loaded_at = now()`, key); err != nil {
		return fmt.Errorf("ingest: %s: %w", key, err)
	}
	return nil
}

// Lag is the age of the oldest unloaded file or dirty rollup row (0: none):
// /lag (docs/PIPELINE.md §5.5, refresh.Lag).
func Lag(ctx context.Context, db *pgxpool.Pool) (time.Duration, error) {
	return refresh.Lag(ctx, db)
}
