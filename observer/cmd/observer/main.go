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
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/Blink-Build-Studios/aintercept/observer/internal/events"
	"github.com/Blink-Build-Studios/aintercept/observer/internal/extproc"
)

func main() {
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
	captureFullBodies := os.Getenv("DEV_CAPTURE_FULL_BODIES") == "true"
	captureFile := os.Getenv("OBSERVER_CAPTURE_FILE") // e.g. /data/events.ndjson

	if devLogBodies {
		slog.Warn("DEV_LOG_BODIES enabled — bounded body previews WILL be printed; local development only, never in production",
			"body_preview_cap", bodyPreviewCap)
	}

	// Build the sink topology. Two tiers:
	//   bodyStripped — receive Event.ForStdout() (NO full bodies). stdout and the
	//                  S3/Spaces durable sink live here: full request/response
	//                  CONTENT must never reach the terminal or object storage.
	//   full         — receive the complete event (incl. BodyB64 when capture is
	//                  on). ONLY the local-dev file sink lives here.
	// Routing S3 through the body-stripped tier makes it structurally impossible
	// for decrypted prompt/response content to land in the bucket, regardless of
	// the full-body capture flag.
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
	if bucket := os.Getenv("OBSERVER_S3_BUCKET"); bucket != "" {
		ss, err := events.NewS3Sink(context.Background(), events.S3Config{
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
		})
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

	if captureFullBodies {
		if captureFile == "" {
			fatal("DEV_CAPTURE_FULL_BODIES requires OBSERVER_CAPTURE_FILE — full bodies must go to a file, never stdout or object storage")
		}
		// Belt-and-suspenders: even though S3 is in the body-stripped tier, refuse
		// to run full-body capture with a durable network sink configured at all.
		if s3Sink != nil {
			fatal("DEV_CAPTURE_FULL_BODIES is refused while OBSERVER_S3_BUCKET is set — full request/response content must never be shipped to object storage")
		}
		slog.Warn("DEV_CAPTURE_FULL_BODIES enabled — COMPLETE request/response content is being written to the capture file; dev-only, delete after use, never in production")
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
	if s3Sink != nil && os.Getenv("OBSERVER_KEY_HASH_SALT") == "" {
		fatal("OBSERVER_KEY_HASH_SALT is required when the S3 sink is enabled — without it key_hash is a reversible sha256(key); set a strong random salt")
	}

	procCfg := events.ProcessorConfig{BufferSize: bufferSize}
	if schemaSink != nil {
		procCfg.SchemaSink = schemaSink
		procCfg.SchemaSamplesPerMinute = envInt("OBSERVER_SCHEMA_SAMPLES_PER_MINUTE", 120)
	}
	proc := events.NewProcessor(sink, procCfg)
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
		InstanceID:        instanceID,
		DevLogBodies:      devLogBodies,
		BodyPreviewCap:    bodyPreviewCap,
		CaptureFullBodies: captureFullBodies,
		KeyHashSalt:       os.Getenv("OBSERVER_KEY_HASH_SALT"),
		KeyHintPrefix:     envInt("OBSERVER_KEY_HINT_PREFIX", 6),
		KeyHintSuffix:     envInt("OBSERVER_KEY_HINT_SUFFIX", 4),
	})

	// Bound concurrent ext_proc streams (one per in-flight HTTP transaction) so
	// total per-stream body buffering (up to reqBufferCap+respBufferCap each)
	// can't exhaust memory under a flood of large concurrent requests.
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
	httpSrv := &http.Server{Addr: httpAddr, Handler: statsMux(proc)}
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
	slog.Info("shutting down, draining telemetry buffer")
	stopStats()
	stopFlush()
	grpcServer.GracefulStop()
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

func statsMux(proc *events.Processor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(proc.Stats())
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
