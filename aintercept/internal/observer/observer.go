// observer is the Go telemetry consumer. It runs an Envoy ext_proc gRPC server,
// acks every message immediately, and processes observations off the request
// path via a bounded, drop-on-overflow buffer. v0 prints newline-delimited JSON
// events to stdout; the sink is pluggable for a future durable (S3) sink.
//
// Design constraints honored here:
//   - Non-blocking: the request path never waits on processing or the sink.
//   - Horizontally scalable, no sticky routing: each instance is self-contained
//     and tags events with its instance ID and a globally-unique correlation ID.
//   - Fail-shed, loudly: a full buffer drops events and raises alarms rather
//     than stalling traffic.
package observer

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/extproc"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/probe"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

func Run() {
	// Structured JSON logs to STDERR (telemetry events go to stdout via StdoutSink,
	// so the two streams stay cleanly separated). Level is INFO by default,
	// overridable via OBSERVER_LOG_LEVEL (debug|info|warn|error). Every log line the
	// observer emits is a JSON object so logs are machine-parseable in aggregation.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel(os.Getenv("OBSERVER_LOG_LEVEL")),
	})))

	grpcAddr := envDefault("OBSERVER_GRPC_LISTEN", "0.0.0.0:9000")
	httpAddr := envDefault("OBSERVER_HTTP_LISTEN", "0.0.0.0:9001")
	instanceID := envDefault("OBSERVER_INSTANCE", defaultInstanceID())
	bufferSize := envInt("OBSERVER_BUFFER_SIZE", 8192)
	bodyPreviewCap := envInt("DEV_BODY_PREVIEW_CAP", 256)
	devLogBodies := os.Getenv("DEV_LOG_BODIES") == "true"
	// The deployment's interception mode: content-free (metadata only, the
	// default) or full (every request and response body is recorded too).
	interceptionMode := envDefault("INTERCEPTION_MODE", "content-free")
	if interceptionMode != "content-free" && interceptionMode != "full" {
		fatal("INTERCEPTION_MODE must be content-free or full", "got", interceptionMode)
	}
	captureFullBodies := interceptionMode == "full"
	captureFile := os.Getenv("OBSERVER_CAPTURE_FILE") // e.g. /data/events.ndjson

	slog.Info("interception mode", "mode", interceptionMode)
	if devLogBodies {
		slog.Warn("DEV_LOG_BODIES enabled: bounded response-body previews are printed to the log (a debugging aid)",
			"body_preview_cap", bodyPreviewCap)
	}

	// Build the sink topology. Two tiers:
	//   bodyStripped — receive Event.ForStdout() (no bodies): stdout and the
	//                  content-free stream (S3/Spaces, local dir). Written in
	//                  both interception modes.
	//   full         — receive the complete event, including BodyB64 in full
	//                  interception mode: the capture file and the capture/
	//                  stream.
	stdoutSink := events.NewStdoutSink(os.Stdout)
	var sink events.Sink = stdoutSink

	var bodyStripped = []events.Sink{stdoutSink}
	var full []events.Sink

	var fileSink *events.FileSink
	if captureFile != "" {
		fs, err := events.NewFileSink(captureFile)
		if err != nil {
			fatal("capture file open failed", "path", captureFile, "err", err)
		}
		fileSink = fs
		full = append(full, fileSink) // the ONLY sink that may receive full bodies
		slog.Info("capturing full-fidelity events to file", "path", captureFile)
	}

	// The writer: the one way to object storage (or the local directory). Every
	// durable sink below is a stream of it. It seals on a ticker and a byte cap
	// into an on-disk spool and uploads from there, retrying until the store
	// accepts each file (docs/STREAMING_PIPELINE.md, phase 1).
	bucket := os.Getenv("OBSERVER_S3_BUCKET")
	localDir := os.Getenv("OBSERVER_LOCAL_DIR")
	var writer *events.Writer
	var targets []events.Target
	var flagger atomic.Pointer[keyflag.Flagger]
	if bucket != "" {
		// On AWS, leave the keys unset to use IRSA or Pod Identity; on
		// DigitalOcean Spaces (or MinIO), set OBSERVER_S3_ENDPOINT + static keys.
		t, err := events.NewS3Target(context.Background(), "s3", events.S3TargetConfig{
			Bucket:          bucket,
			Region:          os.Getenv("OBSERVER_S3_REGION"),
			Endpoint:        os.Getenv("OBSERVER_S3_ENDPOINT"),
			AccessKeyID:     os.Getenv("OBSERVER_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("OBSERVER_S3_SECRET_ACCESS_KEY"),
			ForcePathStyle:  os.Getenv("OBSERVER_S3_FORCE_PATH_STYLE") == "true",
		})
		if err != nil {
			fatal("s3 target init failed", "err", err)
		}
		targets = append(targets, t)
	}
	if localDir != "" {
		t, err := events.NewDirTarget("local", localDir)
		if err != nil {
			fatal("local target init failed", "dir", localDir, "err", err)
		}
		targets = append(targets, t)
	}
	if len(targets) > 0 {
		spoolDir := envDefault("OBSERVER_SPOOL_DIR", filepath.Join(os.TempDir(), "aintercept-spool"))
		w, err := events.NewWriter(events.WriterConfig{
			SpoolDir:      spoolDir,
			SpoolMaxBytes: int64(envInt("OBSERVER_SPOOL_MAX_BYTES", 0)),
			FlushEvery:    time.Duration(envInt("OBSERVER_S3_MAX_AGE_SECONDS", 10)) * time.Second,
			UploadTimeout: time.Duration(envInt("OBSERVER_UPLOAD_TIMEOUT_SECONDS", 0)) * time.Second,
			Targets:       targets,
			// Each landed ingest file flags its key (keyflag): set below, once
			// the flagger exists; files a previous process left upload from now.
			OnUploaded: func(target, key string) {
				if f := flagger.Load(); f != nil {
					f.Landed(target, key)
				}
			},
		})
		if err != nil {
			fatal("writer init failed", "spool", spoolDir, "err", err)
		}
		writer = w
		slog.Info("writer enabled", "spool", spoolDir, "flush_every_seconds", envInt("OBSERVER_S3_MAX_AGE_SECONDS", 10))
	}
	streamCfg := func(target, prefix string) events.S3Config {
		return events.S3Config{
			Target:     target,
			Prefix:     prefix,
			InstanceID: instanceID,
			MaxBytes:   envInt("OBSERVER_S3_MAX_BYTES", 0),
			MaxEvents:  envInt("OBSERVER_S3_MAX_EVENTS", 0),
		}
	}

	// The content-free stream in object storage. ALWAYS body-stripped.
	var s3Sink *events.S3Sink
	if bucket != "" {
		s3Sink = events.NewS3Sink(writer, streamCfg("s3", os.Getenv("OBSERVER_S3_PREFIX")))
		bodyStripped = append(bodyStripped, s3Sink) // body-stripped tier: never full content
		slog.Info("durable S3 sink enabled",
			"bucket", bucket,
			"region", os.Getenv("OBSERVER_S3_REGION"),
			"endpoint", os.Getenv("OBSERVER_S3_ENDPOINT"))
	}

	// Local partitioned sink (a stack on one machine; docs/ui/BUILD_PLAN.md §5):
	// the S3 sink's key layout in a directory instead of a bucket, so the query
	// API reads it like object storage. ALWAYS body-stripped.
	var localSink *events.S3Sink
	if localDir != "" {
		localSink = events.NewS3Sink(writer, streamCfg("local", ""))
		bodyStripped = append(bodyStripped, localSink)
		slog.Info("local partitioned sink enabled", "dir", localDir)
	}

	// Full interception (INTERCEPTION_MODE=full). Full events go to the capture
	// file and, when the S3 or local sink is configured, under <prefix>/capture/,
	// which the processor reads in this mode. The content-free stream is written
	// alongside it either way.
	if captureFullBodies {
		if captureFile == "" && s3Sink == nil && localSink == nil {
			fatal("INTERCEPTION_MODE=full needs OBSERVER_CAPTURE_FILE, the S3 sink or OBSERVER_LOCAL_DIR to write bodies to; never stdout")
		}
		if localSink != nil {
			cfg := streamCfg("local", "capture")
			cfg.IncludeContent = true
			full = append(full, events.NewS3Sink(writer, cfg))
			slog.Info("full interception: request and response bodies are recorded locally", "dir", filepath.Join(localDir, "capture"))
		}
		if s3Sink != nil {
			cfg := streamCfg("s3", strings.Trim(os.Getenv("OBSERVER_S3_PREFIX")+"/capture", "/"))
			cfg.IncludeContent = true
			full = append(full, events.NewS3Sink(writer, cfg))
			slog.Info("full interception: request and response bodies are recorded to object storage", "bucket", bucket, "prefix", cfg.Prefix)
		}
	}

	// Canonical records, per routing key (docs/PIPELINE.md §1): each key's
	// records are buffered (a spool file per key) and written as ingest files
	// under v1/keys/<key>/in/, beside the raw log, through the same bucket or
	// directory; each landed file flags its key for the rollups (keyflag). From
	// the full rows when bodies are recorded (a call's detail is read from
	// them), else from the body-stripped ones.
	budget := setupMemory()
	var recordsSink *records.Sink
	if os.Getenv("OBSERVER_RECORDS") != "false" {
		var put records.Putter
		prefix, target := "", ""
		switch {
		case s3Sink != nil:
			target, prefix = "s3", os.Getenv("OBSERVER_S3_PREFIX")
		case localSink != nil:
			target = "local"
		}
		if target != "" {
			put = records.WriterPutter(writer, target)
			inc := records.NewIncarnation(instanceID)
			recent := writer.NewObjectStream(events.ObjectStreamConfig{Target: target, ContentType: "application/json"})
			f := keyflag.New(keyflag.Config{Redis: os.Getenv("OBSERVER_REDIS_ADDR"), Prefix: prefix, Incarnation: inc,
				Put: recent.Put})
			flagger.Store(f)
			go f.Run(context.Background())
			spoolDir := envDefault("OBSERVER_SPOOL_DIR", filepath.Join(os.TempDir(), "aintercept-spool"))
			recordsSink = records.NewSink(records.SinkConfig{Putter: put, Prefix: prefix, Budget: budget,
				Incarnation: inc, Content: captureFullBodies, Details: true,
				MaxAge:         time.Duration(envInt("OBSERVER_S3_MAX_AGE_SECONDS", 10)) * time.Second,
				KeySpoolDir:    filepath.Join(spoolDir, "keys"),
				KeyObject:      func(key string, seq int64) string { return keyflag.InTemplate(prefix, key, inc, seq) },
				FlushBytes:     envInt("OBSERVER_KEY_FLUSH_BYTES", 1<<20),
				FlushAge:       time.Duration(envInt("OBSERVER_KEY_FLUSH_SECONDS", 30)) * time.Second,
				MaxBufferBytes: int64(envInt("OBSERVER_KEY_BUFFER_MAX_BYTES", 64<<20))})
			if captureFullBodies {
				full = append(full, recordsSink)
			} else {
				bodyStripped = append(bodyStripped, recordsSink)
			}
			go recordsSink.Run(context.Background())
			slog.Info("per-key ingest files enabled", "prefix", prefix, "redis", os.Getenv("OBSERVER_REDIS_ADDR") != "",
				"flush_bytes", envInt("OBSERVER_KEY_FLUSH_BYTES", 1<<20), "flush_seconds", envInt("OBSERVER_KEY_FLUSH_SECONDS", 30))
		}
	}

	if len(full) > 0 || len(bodyStripped) > 1 {
		sink = events.NewMultiSink(bodyStripped, full)
	}

	// Unrecognized-shape sampling sink. When the durable S3 sink is on (and sampling
	// isn't explicitly disabled), structure-only fingerprints of transactions we did
	// NOT fully recognize are written to the SAME bucket under a schema-samples/
	// prefix — apart from normal telemetry so a new provider/schema is easy to find
	// and process without touching the main stream. Fingerprints carry no content.
	var schemaSink *events.S3Sink
	schemaSamplingOn := s3Sink != nil && os.Getenv("OBSERVER_SCHEMA_SAMPLING") != "false"
	if schemaSamplingOn {
		basePrefix := strings.Trim(os.Getenv("OBSERVER_S3_PREFIX"), "/")
		samplePrefix := "schema-samples"
		if basePrefix != "" {
			samplePrefix = basePrefix + "/schema-samples"
		}
		schemaSink = events.NewS3Sink(writer, events.S3Config{Target: "s3", Prefix: samplePrefix, InstanceID: instanceID})
		slog.Info("unrecognized-shape sampling enabled",
			"prefix", samplePrefix,
			"samples_per_minute", envInt("OBSERVER_SCHEMA_SAMPLES_PER_MINUTE", 120))
	}

	// Refuse to ship pseudonymous identity to a durable network sink without a
	// salt: an unsalted key_hash is a reversible sha256(key). Defense-in-depth
	// alongside the Helm chart's install-time guard.
	keyHashSalt := loadKeyHashSalt()
	if s3Sink != nil && keyHashSalt == "" {
		fatal("a key-hash salt (OBSERVER_KEY_HASH_SALT or OBSERVER_KEY_HASH_SALT_SECRET_ID) is required when the S3 sink is enabled — without it key_hash is a reversible sha256(key); set a strong random salt")
	}

	procCfg := events.ProcessorConfig{BufferSize: bufferSize, Budget: budget}
	var spoolFull func() bool
	if writer != nil {
		spoolFull = writer.Full
		procCfg.Full = spoolFull
	}
	if schemaSink != nil {
		procCfg.SchemaSink = schemaSink
		procCfg.SchemaSamplesPerMinute = envInt("OBSERVER_SCHEMA_SAMPLES_PER_MINUTE", 120)
	}
	// The customer's Interceptor, when configured: every call's start, content
	// (when enabled) and record go to it; its verdicts are recorded as rows.
	var proc *events.Processor
	ic := setupInterceptor(instanceID, func(e events.Event) { proc.Offer(e) })
	if ic != nil {
		procCfg.OnComplete = func(e *events.Event) {
			if !e.Synthetic {
				rec, final := interceptor.Record(e)
				ic.Complete(rec, final)
			}
		}
	}
	proc = events.NewProcessor(sink, procCfg)
	stopStats := proc.StartStatsLogger(30 * time.Second)

	// Periodically flush the capture file so a live `tail -f` sees events
	// promptly and an ungraceful kill loses at most ~1s of capture, rather than
	// stranding everything in the write buffer until shutdown.
	stopFlush := func() {}
	if fileSink != nil {
		ticker := time.NewTicker(1 * time.Second)
		doneFlush := make(chan struct{})
		go func() {
			for {
				select {
				case <-doneFlush:
					return
				case <-ticker.C:
					if err := fileSink.Flush(); err != nil {
						slog.Error("periodic capture flush error", "err", err)
					}
				}
			}
		}()
		stopFlush = func() { ticker.Stop(); close(doneFlush) }
	}

	srv := extproc.New(extproc.Config{
		Processor:         proc,
		Budget:            budget,
		InstanceID:        instanceID,
		DevLogBodies:      devLogBodies,
		BodyPreviewCap:    bodyPreviewCap,
		CaptureFullBodies: captureFullBodies,
		KeyHashSalt:       keyHashSalt,
		KeyHintPrefix:     envInt("OBSERVER_KEY_HINT_PREFIX", 6),
		KeyHintSuffix:     envInt("OBSERVER_KEY_HINT_SUFFIX", 4),
		// Comma-separated request headers trusted to carry the client IP (e.g.
		// cf-connecting-ip behind an L7 proxy that sets it). Unset: use only the
		// connection's peer address, which a client cannot forge.
		ClientIPHeaders: envList("OBSERVER_CLIENT_IP_HEADERS"),
		ProbeMarker:     probeMarker(keyHashSalt),
		Interceptor:     ic,
		Full:            spoolFull,
	})

	// Bound concurrent ext_proc streams (one per in-flight transaction). This
	// caps work, not memory: what streams hold is measured by the memory
	// budget.
	maxStreams := uint32(envInt("OBSERVER_MAX_CONCURRENT_STREAMS", 2048))
	grpcServer := grpc.NewServer(grpc.MaxConcurrentStreams(maxStreams))
	extprocv3.RegisterExternalProcessorServer(grpcServer, srv)

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		fatal("grpc listen failed", "addr", grpcAddr, "err", err)
	}

	// HTTP side: liveness + stats, for probes and operators.
	mux := statsMux(proc, budget, ic, writer)
	mux.HandleFunc("/prestop", preStop(envDefault("OBSERVER_ENVOY_ADMIN", "127.0.0.1:9901"),
		time.Duration(envInt("OBSERVER_PRESTOP_TIMEOUT_SECONDS", 600))*time.Second))
	stopPathProbe := startPathProbe(mux, keyHashSalt)
	httpSrv := &http.Server{Addr: httpAddr, Handler: mux}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
		}
	}()

	slog.Info("observer started",
		"instance", instanceID,
		"grpc_addr", grpcAddr,
		"http_addr", httpAddr,
		"buffer_size", bufferSize,
		"dev_log_bodies", devLogBodies)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			fatal("grpc serve failed", "err", err)
		}
	}()

	// Graceful shutdown: stop accepting, drain buffer, flush sink.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	// Keep serving while Envoy learns this pod is going: it is gone from the headless
	// Service's DNS once terminating, and Envoy re-resolves every 5 s. Stopping at once
	// refused the ext_proc streams still routed here, and Envoy (failure_mode_allow)
	// passed those calls through unrecorded: "reported, not captured".
	if d := envInt("OBSERVER_SHUTDOWN_DELAY_SECONDS", 15); d > 0 {
		slog.Info("terminating: serving on while routing moves away", "seconds", d)
		time.Sleep(time.Duration(d) * time.Second)
	}
	slog.Info("shutting down, draining telemetry buffer")
	stopStats()
	stopFlush()
	stopPathProbe()
	grpcServer.GracefulStop()
	if ic != nil {
		ic.Close() // delivers queued callbacks (bounded by their timeout)
	}
	proc.Close() // drains buffer and flushes sinks
	if fileSink != nil {
		if err := fileSink.Close(); err != nil {
			slog.Error("capture file close error", "err", err)
		}
	}
	if f := flagger.Load(); f != nil && writer != nil {
		// Every key buffer is in the spool now (proc.Close flushed the records
		// sink): upload it, then the recent index naming those files.
		if err := writer.Flush(); err != nil {
			slog.Error("writer flush error", "err", err)
		}
		if err := f.FlushRecent(); err != nil {
			slog.Error("recent index upload failed at shutdown; the files are still flagged in Redis", "err", err)
		}
		f.Close()
	}
	if writer != nil {
		// Seals what's buffered and waits until the spool is empty, so a graceful
		// shutdown uploads everything. Whatever can't be uploaded before the pod
		// is killed stays in the spool for the next observer on it.
		slog.Info("flushing the writer", "spool_files", writer.Stats().SpoolFiles)
		if err := writer.Close(); err != nil {
			slog.Error("writer close error", "err", err)
		}
	}
	_ = httpSrv.Close()
	s := proc.Stats()
	slog.Info("final stats",
		"accepted", s.Accepted,
		"emitted", s.Emitted,
		"last_resort_drops", s.LastResortDrops,
		"panics", s.Panics,
		"emit_errs", s.EmitErrs,
		"schema_sampled", s.SchemaSampled,
		"schema_cap_hit", s.SchemaCapHit)
}

// fatal logs a structured error and exits non-zero. Used in place of
// log.Fatalf so startup failures are emitted as JSON like every other log line.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// logLevel parses an OBSERVER_LOG_LEVEL string into a slog.Level, defaulting to
// INFO for empty/unrecognized values.
func logLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// loadKeyHashSalt returns the key-hash salt: OBSERVER_KEY_HASH_SALT, or else
// the AWS Secrets Manager secret OBSERVER_KEY_HASH_SALT_SECRET_ID (name or
// ARN), read once at startup with the standard AWS credential chain (EKS Pod
// Identity or IRSA). Region comes from AWS_REGION, else OBSERVER_S3_REGION.
func loadKeyHashSalt() string {
	if v := os.Getenv("OBSERVER_KEY_HASH_SALT"); v != "" {
		return v
	}
	secretID := os.Getenv("OBSERVER_KEY_HASH_SALT_SECRET_ID")
	if secretID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var opts []func(*config.LoadOptions) error
	if os.Getenv("AWS_REGION") == "" {
		opts = append(opts, config.WithRegion(os.Getenv("OBSERVER_S3_REGION")))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		fatal("load AWS config for the key-hash salt", "err", err)
	}
	salt, err := fetchSalt(ctx, secretsmanager.NewFromConfig(awsCfg), secretID)
	if err != nil {
		fatal("read key-hash salt from Secrets Manager", "err", err)
	}
	slog.Info("key-hash salt loaded from Secrets Manager", "secret", secretID)
	return salt
}

// startPathProbe enables the interception-path health check when
// OBSERVER_PROBE_HOSTS is set: it probes each host through Envoy (see
// internal/probe) and serves the verdict at /healthz/path. It needs only the
// public interception CA chain. Returns a stop function.
func startPathProbe(mux *http.ServeMux, salt string) func() {
	// OBSERVER_PROBE_TARGETS overrides what is requested per allowlist entry
	// ("entry=host/path"): a gentle path for bot-protected web hosts, a
	// concrete host for a wildcard entry (skipped without one).
	targets := probe.ParseTargets(envList("OBSERVER_PROBE_HOSTS"), envList("OBSERVER_PROBE_TARGETS"))
	if len(targets) == 0 {
		return func() {}
	}
	rootsPath := envDefault("OBSERVER_PROBE_ROOTS", "/etc/aintercept/ca/ca-chain.crt")
	pemBytes, err := os.ReadFile(rootsPath)
	if err != nil {
		fatal("path probe: read roots", "path", rootsPath, "err", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		fatal("path probe: no certificates in roots file", "path", rootsPath)
	}
	m := &probe.Monitor{
		Prober: &probe.Prober{
			Target:          envDefault("OBSERVER_PROBE_TARGET", "envoy:443"),
			Roots:           roots,
			MinLeafValidity: envDuration("OBSERVER_PROBE_MIN_LEAF_VALIDITY", 30*time.Minute),
			Timeout:         envDuration("OBSERVER_PROBE_TIMEOUT", 10*time.Second),
			Marker:          probeMarker(salt),
		},
		Targets:  targets,
		Interval: envDuration("OBSERVER_PROBE_INTERVAL", 15*time.Second),
	}
	mux.Handle("/healthz/path", m)
	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx)
	slog.Info("path probe enabled", "target", m.Prober.Target, "targets", targets, "interval", m.Interval)
	return cancel
}

func statsMux(proc *events.Processor, budget *membudget.Budget, ic *interceptor.Client, writer *events.Writer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// Readiness never depends on the spool. A full spool fails closed only for
	// enforced calls, which are refused per stream; readiness gates the whole
	// pod, and observed traffic must keep flowing. Fullness is on /stats
	// (writer.full, LastResortDrops) for alerting.
	mux.HandleFunc("/healthz/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var ics *interceptor.Stats
		if ic != nil {
			s := ic.Stats()
			ics = &s
		}
		var ws *events.WriterStats
		if writer != nil {
			s := writer.Stats()
			ws = &s
		}
		_ = json.NewEncoder(w).Encode(struct {
			events.Stats
			Memory      budgetStats         `json:"memory"`
			Interceptor *interceptor.Stats  `json:"interceptor,omitempty"`
			Writer      *events.WriterStats `json:"writer,omitempty"`
		}{proc.Stats(), statsOf(budget), ics, ws})
	})
	return mux
}

func defaultInstanceID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "observer"
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("invalid integer env var, using default", "key", key, "value", v, "default", def)
	}
	return def
}

// envList splits a comma-separated env var into trimmed, non-empty entries.
func envList(key string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(key), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// probeMarker is the synthetic-traffic marker, or "" (tagging off) without a
// salt: an unsalted marker would be a public constant any client could send
// to have its traffic excluded as synthetic.
func probeMarker(salt string) string {
	if salt == "" {
		return ""
	}
	return probe.Marker(salt)
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		slog.Warn("invalid duration env var, using default", "key", key, "value", v, "default", def)
	}
	return def
}

// preStop is the observer container's preStop hook (an httpGet), for the pod
// it shares with Envoy: it returns once Envoy has exited, so the observer keeps
// recording every stream Envoy still serves while it drains, and only then
// gets SIGTERM. Envoy is gone when its admin port refuses connections.
func preStop(envoyAdmin string, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			c, err := net.DialTimeout("tcp", envoyAdmin, time.Second)
			if err != nil {
				slog.Info("preStop: Envoy has exited; shutting down", "envoy_admin", envoyAdmin)
				w.WriteHeader(http.StatusOK)
				return
			}
			_ = c.Close()
			time.Sleep(time.Second)
		}
		slog.Warn("preStop: Envoy still up at the deadline; shutting down anyway", "envoy_admin", envoyAdmin)
		w.WriteHeader(http.StatusOK)
	}
}
