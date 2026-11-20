package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
)

// pgCmd is `aintercept pg reset` (docs/PRODUCTION.md §5b3): it empties the
// database WORKER_POSTGRES_URL names and applies this build's migrations, as a
// `-- reset` migration does. The workers, running or not, then reload every
// raw file still in the bucket.
func pgCmd() {
	if len(os.Args) != 3 || os.Args[2] != "reset" {
		fmt.Fprintln(os.Stderr, "usage: aintercept pg reset   (empties the database WORKER_POSTGRES_URL names; the workers reload every raw file)")
		os.Exit(2)
	}
	dsn := os.Getenv("WORKER_POSTGRES_URL")
	if dsn == "" {
		fatalf("pg reset: WORKER_POSTGRES_URL is required (the worker login's DSN)")
	}
	ctx := context.Background()
	db, err := pg.Open(ctx, dsn, 2)
	if err != nil {
		fatalf("pg reset: %v", err)
	}
	defer db.Close()
	if err := pg.Reset(ctx, db); err != nil {
		fatalf("pg reset: %v", err)
	}
	fmt.Printf("pg reset: the database is empty, at schema version %d; the workers reload every raw file\n", pg.Latest)
}
