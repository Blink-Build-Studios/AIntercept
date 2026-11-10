package schemareport

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3API is the minimal S3 surface the reader needs, so tests inject a fake.
type s3API interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// ScanConfig configures a bucket scan.
type ScanConfig struct {
	Bucket string
	Prefix string // e.g. "aintercept/schema-samples" or "schema-samples"
	// SinceNS skips objects last-modified at/before this nanosecond watermark, so
	// a re-run only reads new samples. 0 scans everything.
	SinceNS int64
}

// Scan lists every schema-sample object under the prefix, reads the ones newer
// than the watermark, gunzips them, and folds each NDJSON line into agg. It
// returns the number of objects read. Per-object read/parse errors are returned
// so the caller can surface partial-data honestly rather than silently dropping.
func Scan(ctx context.Context, client s3API, cfg ScanConfig, agg *Aggregator) (objectsRead int, err error) {
	var token *string
	sinceTime := time.Unix(0, cfg.SinceNS)
	for {
		out, lerr := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(cfg.Bucket),
			Prefix:            aws.String(cfg.Prefix),
			ContinuationToken: token,
		})
		if lerr != nil {
			return objectsRead, fmt.Errorf("list objects: %w", lerr)
		}
		for _, obj := range out.Contents {
			// LastModified is a cheap pre-filter: skip objects that cannot contain
			// samples newer than the cursor without downloading them. The per-line
			// TimestampNS check in Add is the authoritative filter.
			if cfg.SinceNS > 0 && obj.LastModified != nil && !obj.LastModified.After(sinceTime) {
				continue
			}
			if obj.Key == nil {
				continue
			}
			if rerr := readObject(ctx, client, cfg.Bucket, *obj.Key, agg); rerr != nil {
				return objectsRead, fmt.Errorf("read %s: %w", *obj.Key, rerr)
			}
			objectsRead++
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return objectsRead, nil
}

// readObject downloads one gzipped NDJSON object and folds its lines into agg.
func readObject(ctx context.Context, client s3API, bucket, key string, agg *Aggregator) error {
	resp, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// Schema-sample objects are always gzipped NDJSON (ContentEncoding gzip). Be
	// tolerant: if the gzip header isn't present, treat the bytes as plain NDJSON.
	var reader io.Reader = bytes.NewReader(raw)
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gr, gerr := gzip.NewReader(bytes.NewReader(raw))
		if gerr != nil {
			return gerr
		}
		defer gr.Close()
		reader = gr
	}

	sc := bufio.NewScanner(reader)
	// NDJSON lines can be large (a wide skeleton); grow the scanner buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		agg.Add(line)
	}
	return sc.Err()
}
