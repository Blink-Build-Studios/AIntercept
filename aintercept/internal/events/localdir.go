package events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DirTarget is the Writer's target for a local directory, in place of the
// object store, for running the stack on one machine (docs/ui/BUILD_PLAN.md
// §5). Sinks write to it exactly as they do to S3, with the same key layout
//
//	<dir>/<prefix>/dt=YYYY-MM-DD/hour=HH/instance=<observer_instance>/<flush-start-ns>-<seq>.ndjson.gz
//
// so the query API reads local and object-storage telemetry the same way.
//
// Each object is written to a hidden temp file in its partition directory and
// renamed into place, so a reader never sees a partial file: it sees the old
// set of files or the new one.
type DirTarget struct {
	name string
	root string
}

// NewDirTarget makes dir (if needed) and returns a target writing under it.
func NewDirTarget(name, dir string) (*DirTarget, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("local target dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("local target dir: %w", err)
	}
	return &DirTarget{name: name, root: abs}, nil
}

func (d *DirTarget) Name() string { return d.name }

func (d *DirTarget) Put(_ context.Context, o Object) error {
	return d.write(o.Key, bytes.NewReader(o.Body))
}

// Exists reports whether key has been written.
func (d *DirTarget) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(filepath.Join(d.root, filepath.FromSlash(key)))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (d *DirTarget) write(key string, body io.Reader) error {
	dst := filepath.Join(d.root, filepath.FromSlash(key))
	// Keys are built by the sinks themselves, but never write outside the root.
	if rel, err := filepath.Rel(d.root, dst); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("local target: key %q escapes %s", key, d.root)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	// The temp name starts with "." and doesn't end in .ndjson.gz, so readers'
	// globs never match it.
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		cleanup()
		return err
	}
	return nil
}
