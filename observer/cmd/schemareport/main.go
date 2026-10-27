// schemareport reads the observer's unrecognized-shape samples from the object
// store and prints a redacted report of every endpoint/body shape we did NOT yet
// fully recognize — so coverage can be extended without anyone handing over
// sensitive data. It is a read-only companion to the observer and reuses the same
// OBSERVER_S3_* environment for bucket access.
//
// Typical hand-off use: a collaborator running the stack in their own account
// runs this and sends back the JSON. It contains structure only (keys + value
// TYPES, never values), and with --redact-keys even object keys are bucketed.
// The printed watermark_ns can be passed as --since on the next run so a re-run
// returns only new shapes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Blink-Build-Studios/aintercept/observer/internal/schemareport"
)

func main() {
	var (
		since      = flag.String("since", "", "only include samples newer than this (RFC3339 timestamp, or an integer unix-nanoseconds watermark from a previous run). Empty = all.")
		redactKeys = flag.Bool("redact-keys", false, "also scrub object KEYS in skeletons to length buckets (belt-and-suspenders; values are always scrubbed regardless)")
		format     = flag.String("format", "json", "output format: json | text")
		prefix     = flag.String("prefix", "", "object key prefix for schema samples (default: <OBSERVER_S3_PREFIX>/schema-samples, else schema-samples)")
		out        = flag.String("out", "", "write the report to this file instead of stdout")
	)
	flag.Parse()

	sinceNS, err := parseSince(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemareport: invalid --since %q: %v\n", *since, err)
		os.Exit(2)
	}

	bucket := os.Getenv("OBSERVER_S3_BUCKET")
	if bucket == "" {
		fmt.Fprintln(os.Stderr, "schemareport: OBSERVER_S3_BUCKET is required")
		os.Exit(2)
	}
	samplePrefix := *prefix
	if samplePrefix == "" {
		base := strings.Trim(os.Getenv("OBSERVER_S3_PREFIX"), "/")
		samplePrefix = "schema-samples"
		if base != "" {
			samplePrefix = base + "/schema-samples"
		}
	}

	client, err := newS3Client(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemareport: s3 client: %v\n", err)
		os.Exit(1)
	}

	agg := schemareport.NewAggregator(*redactKeys, sinceNS)
	objects, scanErr := schemareport.Scan(context.Background(), client, schemareport.ScanConfig{
		Bucket:  bucket,
		Prefix:  samplePrefix,
		SinceNS: sinceNS,
	}, agg)
	report := agg.Finish()
	report.ObjectsScanned = objects

	// Honest partial-data reporting: if the scan failed partway, still emit what we
	// gathered and say so loudly on stderr with a non-zero exit, rather than
	// pretending the report is complete.
	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "schemareport: WARNING scan incomplete after %d objects: %v\n", objects, scanErr)
	}

	if err := writeReport(report, *format, *out); err != nil {
		fmt.Fprintf(os.Stderr, "schemareport: write report: %v\n", err)
		os.Exit(1)
	}

	// Always surface the watermark on stderr so it's easy to copy for --since next
	// run even when the report itself goes to a file.
	fmt.Fprintf(os.Stderr, "schemareport: scanned %d objects, %d samples, %d distinct endpoint shapes. "+
		"Next run: --since %d\n", report.ObjectsScanned, report.SamplesScanned, len(report.Endpoints), report.WatermarkNS)

	if scanErr != nil {
		os.Exit(1)
	}
}

// parseSince accepts an RFC3339 timestamp or a raw integer unix-nanoseconds
// watermark (what a previous run prints). Empty returns 0 (scan all).
func parseSince(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if ns, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ns, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("want RFC3339 or integer nanoseconds")
	}
	return t.UnixNano(), nil
}

// newS3Client builds an S3 client from the same OBSERVER_S3_* env the observer
// uses: endpoint + static keys for DigitalOcean Spaces/MinIO, or the default
// credential chain (IRSA) on AWS when keys are unset.
func newS3Client(ctx context.Context) (*s3.Client, error) {
	region := os.Getenv("OBSERVER_S3_REGION")
	loadOpts := []func(*config.LoadOptions) error{}
	if region != "" {
		loadOpts = append(loadOpts, config.WithRegion(region))
	}
	ak := os.Getenv("OBSERVER_S3_ACCESS_KEY_ID")
	sk := os.Getenv("OBSERVER_S3_SECRET_ACCESS_KEY")
	if ak != "" && sk != "" {
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(ak, sk, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if ep := os.Getenv("OBSERVER_S3_ENDPOINT"); ep != "" {
			o.BaseEndpoint = aws.String(ep)
		}
		if os.Getenv("OBSERVER_S3_FORCE_PATH_STYLE") == "true" {
			o.UsePathStyle = true
		}
	}), nil
}

func writeReport(r schemareport.Report, format, outPath string) error {
	var data []byte
	var err error
	switch format {
	case "json":
		data, err = json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
	case "text":
		data = []byte(renderText(r))
	default:
		return fmt.Errorf("unknown format %q (want json|text)", format)
	}
	if outPath == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(outPath, data, 0o644)
}

// renderText is a compact human summary for a quick eyeball; JSON is the
// authoritative deliverable.
func renderText(r schemareport.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unrecognized-shape report  (generated %s)\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "  objects=%d  samples=%d  endpoints=%d  keys_redacted=%v\n",
		r.ObjectsScanned, r.SamplesScanned, len(r.Endpoints), r.KeysRedacted)
	if r.SinceNS > 0 {
		fmt.Fprintf(&b, "  since=%d\n", r.SinceNS)
	}
	fmt.Fprintf(&b, "  next run: --since %d\n\n", r.WatermarkNS)
	for i, ep := range r.Endpoints {
		fmt.Fprintf(&b, "[%d] %s %s%s  (×%d, reason=%s)\n", i+1, ep.Method, ep.Host, ep.Path, ep.Count, ep.Reason)
		if len(ep.Models) > 0 {
			fmt.Fprintf(&b, "     models: %s\n", strings.Join(ep.Models, ", "))
		}
		fmt.Fprintf(&b, "     statuses: %v\n", ep.Statuses)
		for _, s := range ep.RequestShapes {
			fmt.Fprintf(&b, "     request  [%s ×%d]: %s\n", s.Kind, s.Count, string(s.Skeleton))
		}
		for _, s := range ep.ResponseShapes {
			fmt.Fprintf(&b, "     response [%s ×%d]: %s\n", s.Kind, s.Count, string(s.Skeleton))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
