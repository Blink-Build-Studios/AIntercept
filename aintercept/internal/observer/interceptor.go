package observer

import (
	"fmt"
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
	if err := checkContract(os.Getenv("INTERCEPTOR_CONTRACT")); err != nil {
		fatal("interceptor setup failed", "err", err)
	}
	cfg := interceptor.Config{
		Endpoint:       endpoint,
		Content:        os.Getenv("INTERCEPTOR_CONTENT") == "true",
		InlineLimit:    envInt("INTERCEPTOR_INLINE_LIMIT_BYTES", 0),
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
		TokenFile:      os.Getenv("INTERCEPTOR_TOKEN_FILE"),
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
		"deadline", cfg.Deadline, "on_error", cfg.OnError, "tls", cfg.TLS, "auth", authKind(cfg))
	return ic
}

// checkContract refuses an INTERCEPTOR_CONTRACT other than v2. Contract v1
// was removed: an observer still set to it would talk v2 to a v1 Interceptor
// and get Unimplemented on every call (in enforce mode, with onError deny,
// every call refused).
func checkContract(c string) error {
	if c != "" && c != "v2" {
		return fmt.Errorf("INTERCEPTOR_CONTRACT %q: contract v1 was removed and v2 is the only one; "+
			"unset it and port the Interceptor (docs/INTERCEPTOR.md)", c)
	}
	return nil
}

// authKind names how the observer authenticates to the Interceptor, for the
// startup log line (never the token itself).
func authKind(cfg interceptor.Config) string {
	switch {
	case cfg.TokenFile != "":
		return "token-file"
	case cfg.Token != "":
		return "token"
	}
	return "none"
}
