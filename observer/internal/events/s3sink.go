package events

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Sink is the durable production sink: it batches events in memory and flushes
// each batch as a single gzipped NDJSON object to an S3-compatible object store.
//
// It implements the Sink interface and is designed to be dropped into the
// MultiSink's durable slot alongside (or instead of) FileSink. The hot path is
// unaffected: Emit only appends to an in-memory batch under a short-lived lock
// and never performs network I/O. Uploads happen on a dedicated background
// goroutine, so a slow or failing object store backs up only this sink's batch
// (bounded) and never the ext_proc request path.
//
// ── Object store portability ─────────────────────────────────────────────────
// One sink serves both clouds. It speaks the S3 API, so it targets:
//   - AWS S3 via IRSA (no static keys in the cluster): leave Endpoint empty and
//     AccessKeyID/SecretAccessKey unset — the AWS SDK default credential chain
//     resolves the IRSA web-identity token automatically.
//   - DigitalOcean Spaces (S3-compatible): set Endpoint to the regional Spaces
//     endpoint (e.g. https://nyc3.digitaloceanspaces.com) and provide static
//     access keys. DOKS has no IRSA equivalent.
//
// ── Object key layout (no cross-pod clobbering) ──────────────────────────────
// Every object key is namespaced by the producing replica and a per-replica
// monotonic sequence, under a time partition:
//
//	<prefix>/dt=YYYY-MM-DD/hour=HH/instance=<observer_instance>/<flush-start-ns>-<seq>.ndjson.gz
//
// Because the key carries the unique ObserverInstance plus a per-instance
// sequence number, 100 replicas writing concurrently never collide — no
// coordination, no locking across pods. The dt=/hour= partitions keep the
// layout query-friendly (Athena/Glue/Spark) later.
type S3Sink struct {
	uploader   s3PutObjectAPI
	bucket     string
	prefix     string
	instanceID string

	// Flush thresholds. A batch flushes when it reaches maxBytes OR maxEvents OR
	// maxAge, whichever comes first.
	maxBytes  int
	maxEvents int
	maxAge    time.Duration

	mu         sync.Mutex
	batch      bytes.Buffer // accumulated newline-delimited JSON (uncompressed)
	batchCount int
	batchStart time.Time // wall-clock start of the current batch (for age-based flush)
	seq        uint64    // per-instance monotonic object sequence

	// uploads carries sealed batches to the background uploader goroutine. It is
	// bounded; if the object store is slow enough to fill it, Emit's sealed batch
	// is dropped (loudly) rather than blocking the drain goroutine.
	uploads chan sealedBatch
	wg      sync.WaitGroup
	closeCh chan struct{}
	closed  bool

	// pending counts sealed batches queued-but-not-yet-uploaded, so Flush can
	// block until the uploader has drained everything enqueued so far.
	pending sync.WaitGroup

	droppedBatches uint64
}

// nowFn is the clock, overridable in tests for deterministic age-based flushing.
var nowFn = time.Now

// s3PutObjectAPI is the minimal S3 surface the sink needs, so tests can inject a
// fake without a live endpoint.
type s3PutObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// sealedBatch is a finished batch ready to upload: already-serialized NDJSON and
// the key it will be written under.
type sealedBatch struct {
	key  string
	data []byte
}

// S3Config configures an S3Sink. Zero values fall back to sensible defaults for
// the threshold fields; the store-identity fields are required.
type S3Config struct {
	// Bucket is the S3 bucket / DO Space name. Required.
	Bucket string
	// Prefix is prepended to every object key (e.g. "aintercept"). Optional.
	Prefix string
	// Region is the store region. For AWS, the bucket's region. For DO Spaces,
	// the Space region (e.g. "nyc3"). Required.
	Region string
	// Endpoint overrides the S3 endpoint for S3-compatible stores (DO Spaces,
	// MinIO). Empty => native AWS S3 (and IRSA credentials). Optional.
	Endpoint string
	// AccessKeyID / SecretAccessKey are static credentials for stores without
	// IRSA (DO Spaces, MinIO). Leave both empty on AWS to use the default
	// credential chain (IRSA). Optional.
	AccessKeyID     string
	SecretAccessKey string
	// InstanceID identifies the producing replica; it namespaces object keys so
	// concurrent replicas never clobber each other. Required.
	InstanceID string

	// Flush thresholds (all optional; defaults applied when zero).
	MaxBytes  int           // default 5 MiB
	MaxEvents int           // default 10000
	MaxAge    time.Duration // default 60s

	// UploadQueueDepth bounds in-flight sealed batches awaiting upload. Default 8.
	UploadQueueDepth int
	// ForcePathStyle addresses buckets as <endpoint>/<bucket> instead of
	// <bucket>.<endpoint>. Required by MinIO and some S3-compatible stores.
	ForcePathStyle bool
}

const (
	defaultS3MaxBytes         = 5 << 20 // 5 MiB
	defaultS3MaxEvents        = 10000
	defaultS3MaxAge           = 60 * time.Second
	defaultS3UploadQueueDepth = 8
)

// NewS3Sink builds the S3 client from cfg and starts the background uploader.
// Call Close (directly or via MultiSink) on shutdown to flush the final batch
// and drain in-flight uploads.
func NewS3Sink(ctx context.Context, cfg S3Config) (*S3Sink, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 sink: bucket is required")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("s3 sink: region is required")
	}
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("s3 sink: instance id is required")
	}

	loadOpts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	// Static creds only when provided (DO Spaces / MinIO). On AWS we deliberately
	// leave this unset so the default chain resolves IRSA — no keys in-cluster.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3 sink: load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		if cfg.ForcePathStyle {
			o.UsePathStyle = true
		}
	})

	return newS3SinkWithClient(client, cfg), nil
}

// newS3SinkWithClient is the test seam: it builds the sink around an injected
// PutObject implementation and starts the uploader.
func newS3SinkWithClient(client s3PutObjectAPI, cfg S3Config) *S3Sink {
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultS3MaxBytes
	}
	maxEvents := cfg.MaxEvents
	if maxEvents <= 0 {
		maxEvents = defaultS3MaxEvents
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = defaultS3MaxAge
	}
	depth := cfg.UploadQueueDepth
	if depth <= 0 {
		depth = defaultS3UploadQueueDepth
	}

	s := &S3Sink{
		uploader:   client,
		bucket:     cfg.Bucket,
		prefix:     strings.Trim(cfg.Prefix, "/"),
		instanceID: cfg.InstanceID,
		maxBytes:   maxBytes,
		maxEvents:  maxEvents,
		maxAge:     maxAge,
		uploads:    make(chan sealedBatch, depth),
		closeCh:    make(chan struct{}),
	}

	// Background uploader: performs the actual (blocking) network PUTs so the
	// drain goroutine never waits on the object store.
	s.wg.Add(1)
	go s.uploadLoop()

	// Age-based flusher: ensures low-traffic replicas still flush on time even
	// when they never reach the size/count thresholds.
	s.wg.Add(1)
	go s.ageLoop()

	return s
}

// Emit appends one event to the current batch. It never performs network I/O;
// when a size/count threshold is crossed it seals the batch and hands it to the
// background uploader. Honors the Sink contract (non-blocking on the hot path).
func (s *S3Sink) Emit(e Event) error {
	// Defense in depth: the S3 sink physically cannot persist decrypted content.
	// Even if a future wiring change routed full events here, body fields are
	// cleared before serialization. Object storage gets structured metadata only.
	b, err := json.Marshal(e.WithoutContent())
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("s3 sink: emit after close")
	}
	if s.batchCount == 0 {
		s.batchStart = nowFn()
	}
	s.batch.Write(b)
	s.batch.WriteByte('\n')
	s.batchCount++

	if s.batch.Len() >= s.maxBytes || s.batchCount >= s.maxEvents {
		s.sealLocked()
	}
	return nil
}

// Flush seals the current (possibly partial) batch and blocks until every batch
// enqueued so far has been uploaded (or dropped). Called on shutdown by the
// processor/MultiSink, and usable as a test barrier.
func (s *S3Sink) Flush() error {
	s.mu.Lock()
	s.sealLocked()
	s.mu.Unlock()
	s.pending.Wait()
	return nil
}

// Close flushes the final batch, stops the background goroutines, and waits for
// in-flight uploads to finish. Idempotent.
func (s *S3Sink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.sealLocked()
	s.closed = true
	s.mu.Unlock()

	close(s.closeCh) // stop ageLoop
	close(s.uploads) // uploadLoop drains remaining items then returns
	s.wg.Wait()
	if s.droppedBatches > 0 {
		slog.Warn("ALARM s3 sink: batches dropped due to a full upload queue (object store too slow); those events were not persisted",
			"dropped_batches", s.droppedBatches)
	}
	return nil
}

// sealLocked seals the current batch and enqueues it for upload. Caller holds mu.
// A sealed batch with no events is a no-op. If the upload queue is full, the
// batch is dropped (loudly) rather than blocking the caller (the drain goroutine
// or shutdown) on a slow store.
func (s *S3Sink) sealLocked() {
	if s.batchCount == 0 {
		return
	}
	data := make([]byte, s.batch.Len())
	copy(data, s.batch.Bytes())
	key := s.nextKeyLocked()

	s.batch.Reset()
	s.batchCount = 0

	s.pending.Add(1)
	select {
	case s.uploads <- sealedBatch{key: key, data: data}:
	default:
		s.pending.Done()
		s.droppedBatches++
		slog.Warn("ALARM s3 sink: upload queue full — dropping a sealed batch; object store cannot keep up, telemetry being shed, real traffic unaffected",
			"batch_bytes", len(data))
	}
}

// nextKeyLocked builds the object key for the batch about to be sealed. Caller
// holds mu (so seq increments are serialized per instance).
func (s *S3Sink) nextKeyLocked() string {
	s.seq++
	start := s.batchStart
	if start.IsZero() {
		start = nowFn()
	}
	utc := start.UTC()
	var sb strings.Builder
	if s.prefix != "" {
		sb.WriteString(s.prefix)
		sb.WriteByte('/')
	}
	fmt.Fprintf(&sb, "dt=%04d-%02d-%02d/hour=%02d/instance=%s/%d-%06d.ndjson.gz",
		utc.Year(), utc.Month(), utc.Day(), utc.Hour(),
		s.instanceID, utc.UnixNano(), s.seq)
	return sb.String()
}

// uploadLoop performs the blocking PUTs off the hot path. It gzips each sealed
// batch and writes it under its key. On error it logs loudly and moves on —
// telemetry loss never blocks traffic.
func (s *S3Sink) uploadLoop() {
	defer s.wg.Done()
	for sb := range s.uploads {
		if err := s.put(sb); err != nil {
			slog.Warn("ALARM s3 sink: PUT failed — batch not persisted", "key", sb.key, "err", err)
		}
		s.pending.Done()
	}
}

// put gzips and uploads one sealed batch.
func (s *S3Sink) put(sb sealedBatch) error {
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	if _, err := gw.Write(sb.data); err != nil {
		return fmt.Errorf("gzip write: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("gzip close: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.uploader.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(sb.key),
		Body:            bytes.NewReader(gzbuf.Bytes()),
		ContentType:     aws.String("application/x-ndjson"),
		ContentEncoding: aws.String("gzip"),
	})
	return err
}

// ageLoop flushes the current batch when it has been open longer than maxAge, so
// low-traffic replicas don't strand events indefinitely below the size/count
// thresholds.
func (s *S3Sink) ageLoop() {
	defer s.wg.Done()
	// Tick at a fraction of maxAge so worst-case staleness is ~maxAge, not 2x.
	interval := s.maxAge / 2
	if interval <= 0 {
		interval = s.maxAge
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-t.C:
			s.mu.Lock()
			if s.batchCount > 0 && nowFn().Sub(s.batchStart) >= s.maxAge {
				s.sealLocked()
			}
			s.mu.Unlock()
		}
	}
}
