package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// S3Sink is the durable sink: events as gzipped NDJSON objects in object
// storage (or a local directory), written through the Writer, which seals,
// spools and uploads them. Emit never performs I/O; it hands one line to the
// writer's loop.
//
// ── Object key layout (no cross-pod clobbering) ──────────────────────────────
// Every object key is namespaced by the producing replica and a per-replica
// monotonic sequence, under a time partition:
//
//	<prefix>/dt=YYYY-MM-DD/hour=HH/instance=<observer_instance>/<flush-start-ns>-<seq>.ndjson.gz
//
// Because the key carries the unique ObserverInstance plus a per-instance
// sequence number, 100 replicas writing concurrently never collide: no
// coordination, no locking across pods.
type S3Sink struct {
	stream *Stream
	// includeContent keeps body fields (the full-interception capture stream).
	includeContent bool
}

// nowFn is the clock, overridable in tests.
var nowFn = time.Now

// S3Config configures an S3Sink.
type S3Config struct {
	// Target names the Writer target (an S3Target or a DirTarget). Required.
	Target string
	// Prefix is prepended to every object key (e.g. "aintercept"). Optional.
	Prefix string
	// IncludeContent keeps decrypted body fields in the stored events (the
	// full-interception stream, INTERCEPTION_MODE=full).
	IncludeContent bool
	// InstanceID identifies the producing replica; it namespaces object keys so
	// concurrent replicas never clobber each other. Required.
	InstanceID string
	// MaxBytes and MaxEvents seal an object early. The writer also seals every
	// object on its ticker (10 s) and its overall byte cap.
	MaxBytes  int // default 5 MiB
	MaxEvents int // default 10000
}

const (
	defaultS3MaxBytes  = 5 << 20
	defaultS3MaxEvents = 10000
)

// NewS3Sink adds a stream for this sink's key layout to the writer.
func NewS3Sink(w *Writer, cfg S3Config) *S3Sink {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultS3MaxBytes
	}
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = defaultS3MaxEvents
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	instance := cfg.InstanceID
	key := func(seq uint64, start time.Time) string {
		utc := start.UTC()
		var sb strings.Builder
		if prefix != "" {
			sb.WriteString(prefix)
			sb.WriteByte('/')
		}
		fmt.Fprintf(&sb, "dt=%04d-%02d-%02d/hour=%02d/instance=%s/%d-%06d.ndjson.gz",
			utc.Year(), utc.Month(), utc.Day(), utc.Hour(), instance, utc.UnixNano(), seq)
		return sb.String()
	}
	return &S3Sink{
		stream: w.NewStream(StreamConfig{
			Target:      cfg.Target,
			Key:         key,
			MaxBytes:    cfg.MaxBytes,
			MaxEvents:   cfg.MaxEvents,
			Gzip:        true,
			ContentType: "application/x-ndjson",
		}),
		includeContent: cfg.IncludeContent,
	}
}

// Emit hands one event to the writer.
func (s *S3Sink) Emit(e Event) error {
	// Defense in depth: unless this is the explicit full-interception capture
	// sink, body fields are cleared before serialization.
	out := e.WithoutContent()
	if s.includeContent {
		out = e
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return s.stream.Write(b)
}

// Flush seals what's buffered and waits until it's uploaded.
func (s *S3Sink) Flush() error { return s.stream.w.Flush() }
