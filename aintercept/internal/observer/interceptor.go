package observer

import (
	"log/slog"
	"os"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
)

// setupInterceptor connects to the customer's Interceptor (INTERCEPTOR_*;
// docs/INTERCEPTOR.md), or returns nil when INTERCEPTOR_ENDPOINT is unset.
// offer records each verdict as a row in the observer's own stream.
func setupInterceptor(instanceID string, offer func(events.Event)) *interceptor.Client {
	endpoint := os.Getenv("INTERCEPTOR_ENDPOINT")
	if endpoint == "" {
		return nil
	}
	cfg := interceptor.Config{
		Endpoint:       endpoint,
		Content:        os.Getenv("INTERCEPTOR_CONTENT") == "true",
		Deadline:       time.Duration(envInt("INTERCEPTOR_DEADLINE_MS", 50)) * time.Millisecond,
		ObserveTimeout: time.Duration(envInt("INTERCEPTOR_OBSERVE_TIMEOUT_MS", 5000)) * time.Millisecond,
		OnError:        envDefault("INTERCEPTOR_ON_ERROR", "deny"),
		QueueSize:      envInt("INTERCEPTOR_QUEUE_SIZE", 1024),
		Workers:        envInt("INTERCEPTOR_WORKERS", 8),
		TLS:            os.Getenv("INTERCEPTOR_TLS") == "true",
		CAFile:         os.Getenv("INTERCEPTOR_TLS_CA_FILE"),
		CertFile:       os.Getenv("INTERCEPTOR_TLS_CERT_FILE"),
		KeyFile:        os.Getenv("INTERCEPTOR_TLS_KEY_FILE"),
		ServerName:     os.Getenv("INTERCEPTOR_TLS_SERVER_NAME"),
		Token:          os.Getenv("INTERCEPTOR_TOKEN"),
	}
	ic, err := interceptor.New(cfg, func(callID string, v events.Verdict) {
		offer(events.Event{
			CorrelationID: callID, TimestampNS: time.Now().UnixNano(), Phase: events.PhaseVerdict,
			Verdict: &v, ObserverInstance: instanceID,
		})
	})
	if err != nil {
		fatal("interceptor setup failed", "err", err)
	}
	slog.Info("interceptor configured", "endpoint", endpoint, "content", cfg.Content,
		"deadline", cfg.Deadline, "on_error", cfg.OnError, "tls", cfg.TLS)
	return ic
}
