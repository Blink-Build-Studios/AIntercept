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

	// S3-compatible durable sink. Enabled when a bucket is configured. On AWS,
	// leave the keys unset to use IRSA; on DigitalOcean Spaces (or MinIO), set
	// OBSERVER_S3_ENDPOINT + static keys. ALWAYS body-stripped.
	var s3Sink *events.S3Sink
	var s3Cfg events.S3Config
	if bucket := os.Getenv("OBSERVER_S3_BUCKET"); bucket != "" {
		s3Cfg = events.S3Config{
			Bucket:          bucket,
			Prefix:          os.Getenv("OBSERVER_S3_PREFIX"),
			Region:          os.Getenv("OBSERVER_S3_REGION"),
			Endpoint:        os.Getenv("OBSERVER_S3_ENDPOINT"),
			AccessKeyID:     os.Getenv("OBSERVER_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("OBSERVER_S3_SECRET_ACCESS_KEY"),
			InstanceID:      instanceID,
			MaxBytes:        envInt("OBSERVER_S3_MAX_BYTES", 0),
			MaxEvents:       envInt("OBSERVER_S3_MAX_EVENTS", 0),
			MaxAge:          time.Duration(envInt("OBSERVER_S3_MAX_AGE_SECONDS", 0)) * time.Second,
			ForcePathStyle:  os.Getenv("OBSERVER_S3_FORCE_PATH_STYLE") == "true",
		}
		ss, err := events.NewS3Sink(context.Background(), s3Cfg)
		if err != nil {
			fatal("s3 sink init failed", "err", err)
		}
		s3Sink = ss
		bodyStripped = append(bodyStripped, s3Sink) // body-stripped tier: never full content
		slog.Info("durable S3 sink enabled",
			"bucket", bucket,
			"region", os.Getenv("OBSERVER_S3_REGION"),
			"endpoint", os.Getenv("OBSERVER_S3_ENDPOINT"))
	}

	// Local partitioned sink (a stack on one machine; docs/ui/BUILD_PLAN.md §5):
	// the S3 sink's batching and key layout, written to a directory instead of a
	// bucket, so the query API reads it like object storage. ALWAYS body-stripped.
	var localSink *events.S3Sink
	var localCfg events.S3Config
	localDir := os.Getenv("OBSERVER_LOCAL_DIR")
	if localDir != "" {
		localCfg = events.S3Config{
			InstanceID: instanceID,
			MaxBytes:   envInt("OBSERVER_S3_MAX_BYTES", 0),
			MaxEvents:  envInt("OBSERVER_S3_MAX_EVENTS", 0),
			MaxAge:     time.Duration(envInt("OBSERVER_S3_MAX_AGE_SECONDS", 0)) * time.Second,
		}
		ls, err := events.NewLocalDirSink(localDir, localCfg)
		if err != nil {
			fatal("local sink init failed", "dir", localDir, "err", err)
		}
		localSink = ls
		bodyStripped = append(bodyStripped, localSink)
		slog.Info("local partitioned sink enabled", "dir", localDir)
	}

	// Full interception (INTERCEPTION_MODE=full). Full events go to the capture
	// file and, when the S3 or local sink is configured, under <prefix>/capture/,
	// which the processor reads in this mode. The content-free stream is written
	// alongside it either way.
	var captureS3 *events.S3Sink
	var captureLocal *events.S3Sink
	if captureFullBodies {
		if captureFile == "" && s3Sink == nil && localSink == nil {
			fatal("INTERCEPTION_MODE=full needs OBSERVER_CAPTURE_FILE, the S3 sink or OBSERVER_LOCAL_DIR to write bodies to; never stdout")
		}
		if localSink != nil {
			cfg := localCfg
			cfg.Prefix = "capture"
			cfg.IncludeContent = true
			cl, err := events.NewLocalDirSink(localDir, cfg)
			if err != nil {
				fatal("capture local sink init failed", "err", err)
			}
			captureLocal = cl
			full = append(full, captureLocal)
			slog.Info("full interception: request and response bodies are recorded locally", "dir", filepath.Join(localDir, "capture"))
		}
		if s3Sink != nil {
			cfg := s3Cfg
			cfg.Prefix = strings.Trim(cfg.Prefix+"/capture", "/")
			cfg.IncludeContent = true
			cs, err := events.NewS3Sink(context.Background(), cfg)
			if err != nil {
				fatal("capture s3 sink init failed", "err", err)
			}
			captureS3 = cs
			full = append(full, captureS3)
			slog.Info("full interception: request and response bodies are recorded to object storage", "bucket", cfg.Bucket, "prefix", cfg.Prefix)
		}
	}

	// Canonical records (docs/STREAMING_PIPELINE.md §3): the bucketed
	// processor's input, beside the raw log, through the same bucket or
	// directory. From the full rows when bodies are recorded (a call's detail
	// is read from them), else from the body-stripped ones.
	var recordsSink *records.Sink
	if os.Getenv("OBSERVER_RECORDS") != "false" {
		var put records.Putter
		prefix := ""
		switch {
		case s3Sink != nil:
			put, prefix = s3Sink, s3Cfg.Prefix
		case localSink != nil:
			put = localSink
		}
		if put != nil {
			recordsSink = records.NewSink(records.SinkConfig{Putter: put, Prefix: prefix,
				Incarnation: records.NewIncarnation(instanceID), Content: captureFullBodies,
				MaxAge: time.Duration(envInt("OBSERVER_S3_MAX_AGE_SECONDS", 10)) * time.Second})
			if captureFullBodies {
				full = append(full, recordsSink)
			} else {
				bodyStripped = append(bodyStripped, recordsSink)
			}
			go recordsSink.Run(context.Background())
			slog.Info("canonical records enabled", "prefix", records.ObjectKeyPrefix(prefix, 0))
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
		ss, err := events.NewS3Sink(context.Background(), events.S3Config{
			Bucket:          os.Getenv("OBSERVER_S3_BUCKET"),
			Prefix:          samplePrefix,
			Region:          os.Getenv("OBSERVER_S3_REGION"),
			Endpoint:        os.Getenv("OBSERVER_S3_ENDPOINT"),
			AccessKeyID:     os.Getenv("OBSERVER_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("OBSERVER_S3_SECRET_ACCESS_KEY"),
			InstanceID:      instanceID,
			ForcePathStyle:  os.Getenv("OBSERVER_S3_FORCE_PATH_STYLE") == "true",
		})
		if err != nil {
			fatal("schema-sample sink init failed", "err", err)
		}
		schemaSink = ss
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

	budget := setupMemory()
	procCfg := events.ProcessorConfig{BufferSize: bufferSize, Budget: budget}
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
	})

	// Bound concurrent ext_proc streams (one per in-flight transaction). This
	// caps work, not memory: what streams hold is measured by the memory
	// budget, which steers load (ORCA) and drives autoscaling.
	maxStreams := uint32(envInt("OBSERVER_MAX_CONCURRENT_STREAMS", 2048))
	grpcServer := grpc.NewServer(grpc.MaxConcurrentStreams(maxStreams))
	extprocv3.RegisterExternalProcessorServer(grpcServer, srv)
	stopLoadReports := startLoadReports(grpcServer, budget)

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		fatal("grpc listen failed", "addr", grpcAddr, "err", err)
	}

	// HTTP side: liveness + stats, for probes and operators.
	mux := statsMux(proc, budget, ic)
	_, httpPort, _ := net.SplitHostPort(httpAddr)
	mux.Handle("/stats/fleet", fleetHandler(budget, os.Getenv("OBSERVER_PEERS_DNS"), httpPort))
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
	stopLoadReports()
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
	if s3Sink != nil {
		// Close seals the final batch and blocks until in-flight uploads drain,
		// so no captured events are lost on a graceful scale-down.
		if err := s3Sink.Close(); err != nil {
			slog.Error("s3 sink close error", "err", err)
		}
	}
	if captureS3 != nil {
		if err := captureS3.Close(); err != nil {
			slog.Error("capture s3 sink close error", "err", err)
		}
	}
	for name, ls := range map[string]*events.S3Sink{"local": localSink, "capture local": captureLocal} {
		if ls != nil {
			if err := ls.Close(); err != nil {
				slog.Error(name+" sink close error", "err", err)
			}
		}
	}
	if schemaSink != nil {
		if err := schemaSink.Close(); err != nil {
			slog.Error("schema-sample sink close error", "err", err)
		}
	}
	_ = httpSrv.Close()
	s := proc.Stats()
	slog.Info("final stats",
		"accepted", s.Accepted,
		"emitted", s.Emitted,
		"dropped", s.Dropped,
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

func statsMux(proc *events.Processor, budget *membudget.Budget, ic *interceptor.Client) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
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
		_ = json.NewEncoder(w).Encode(struct {
			events.Stats
			Memory      budgetStats        `json:"memory"`
			Interceptor *interceptor.Stats `json:"interceptor,omitempty"`
		}{proc.Stats(), statsOf(budget), ics})
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
