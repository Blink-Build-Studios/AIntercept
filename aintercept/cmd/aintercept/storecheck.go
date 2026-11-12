package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// storecheck is `aintercept storecheck`: whether an object store has what the
// bucketed processor rests on (create-only puts, read- and list-after-write,
// the store's own write times). It writes and deletes one object under -prefix.
func storecheck() {
	fs := flag.NewFlagSet("storecheck", flag.ExitOnError)
	bucket := fs.String("bucket", "", "bucket")
	prefix := fs.String("prefix", "storecheck/", "prefix to write under")
	region := fs.String("region", "us-east-1", "S3 region")
	endpoint := fs.String("endpoint", "", "S3 endpoint (empty: AWS)")
	pathStyle := fs.Bool("path-style", false, "path-style addressing (emulators)")
	fs.Parse(os.Args[2:])
	if *bucket == "" {
		fs.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	// Credentials from the environment (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) or the default chain.
	s, err := process.NewS3Store(ctx, process.S3Config{Bucket: *bucket, Region: *region, Endpoint: *endpoint, ForcePathStyle: *pathStyle})
	if err != nil {
		fatalf("%v", err)
	}
	rep, err := stream.StoreCheck(ctx, stream.S3Store{S3Store: s}, *prefix)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("create-only puts   %v\nread after write   %v\nlist after write   %v\nstore write times  %v\n",
		rep.CreateOnly, rep.ReadAfterWrite, rep.ListAfterWrite, rep.StoreTime)
	if !rep.OK() {
		fatalf("not usable: %s", rep.Detail)
	}
	fmt.Println("ok")
}
