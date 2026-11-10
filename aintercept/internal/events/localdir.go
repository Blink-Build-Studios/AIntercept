package events

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewLocalDirSink is the S3 sink with a local directory in place of the object
// store, for running the stack on one machine (docs/ui/BUILD_PLAN.md §5). It
// shares everything else with S3Sink: batching, flush thresholds, the
// body-stripped tier, and the object key layout
//
//	<dir>/<prefix>/dt=YYYY-MM-DD/hour=HH/instance=<observer_instance>/<flush-start-ns>-<seq>.ndjson.gz
//
// so the query API reads local and object-storage telemetry the same way.
//
// Each object is written to a hidden temp file in its partition directory and
// renamed into place, so a reader never sees a partial file: it sees the old
// set of files or the new one.
func NewLocalDirSink(dir string, cfg S3Config) (*S3Sink, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("local sink dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("local sink dir: %w", err)
	}
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("local sink: InstanceID is required")
	}
	cfg.Bucket = "local"
	return newS3SinkWithClient(dirPutter{root: abs}, cfg), nil
}

// dirPutter implements the one S3 call the sink makes, against a directory.
type dirPutter struct{ root string }

func (d dirPutter) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(in.Key)
	dst := filepath.Join(d.root, filepath.FromSlash(key))
	// Keys are built by the sink itself, but never write outside the root.
	if rel, err := filepath.Rel(d.root, dst); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("local sink: key %q escapes %s", key, d.root)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return nil, err
	}
	// The temp name starts with "." and doesn't end in .ndjson.gz, so readers'
	// globs never match it.
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, in.Body); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		cleanup()
		return nil, err
	}
	return &s3.PutObjectOutput{}, nil
}
