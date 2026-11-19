package observer

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
)

// identityConfig is the credential owners' setup (OBSERVER_IDENTITY_*;
// internal/identity): the cache, its lookups and waits, and the Anthropic
// admin directory.
type identityConfig struct {
	cache                 identity.Config
	wait, enforceWait     time.Duration
	adminKey, adminOrigin string
	directoryEvery        time.Duration
}

func identityFromEnv() identityConfig {
	rate, err := strconv.ParseFloat(os.Getenv("OBSERVER_IDENTITY_LOOKUP_RATE_PER_SECOND"), 64)
	if err != nil {
		rate = 0 // the default
	}
	return identityConfig{
		cache: identity.Config{
			Lookup:        os.Getenv("OBSERVER_IDENTITY_LOOKUP") != "false",
			Providers:     envList("OBSERVER_IDENTITY_LOOKUP_PROVIDERS"),
			Timeout:       time.Duration(envInt("OBSERVER_IDENTITY_LOOKUP_TIMEOUT_MS", 2000)) * time.Millisecond,
			MaxInFlight:   envInt("OBSERVER_IDENTITY_LOOKUP_MAX_IN_FLIGHT", 8),
			RatePerSecond: rate,
			Burst:         envInt("OBSERVER_IDENTITY_LOOKUP_BURST", 20),
			MaxEntries:    envInt("OBSERVER_IDENTITY_CACHE_MAX_ENTRIES", 100_000),
			TTL:           envDuration("OBSERVER_IDENTITY_CACHE_TTL", 24*time.Hour),
			NegativeTTL:   envDuration("OBSERVER_IDENTITY_CACHE_NEGATIVE_TTL", time.Hour),
			ErrorTTL:      envDuration("OBSERVER_IDENTITY_CACHE_ERROR_TTL", time.Minute),
			// Tests only: every lookup (and the directory) goes to this origin.
			Origin: os.Getenv("OBSERVER_IDENTITY_TEST_ORIGIN"),
		},
		wait:           time.Duration(envInt("OBSERVER_IDENTITY_MAX_WAIT_MS", 300)) * time.Millisecond,
		enforceWait:    time.Duration(envInt("OBSERVER_IDENTITY_ENFORCE_MAX_WAIT_MS", 300)) * time.Millisecond,
		adminKey:       os.Getenv("OBSERVER_IDENTITY_ANTHROPIC_ADMIN_KEY"),
		adminOrigin:    os.Getenv("OBSERVER_IDENTITY_TEST_ORIGIN"),
		directoryEvery: envDuration("OBSERVER_IDENTITY_DIRECTORY_INTERVAL", 15*time.Minute),
	}
}

// setupIdentity builds the pod's credential owners. offer records what it
// learns: a credential row (and, for a lookup, an account row) in the
// observer's own stream, which the workers load into credentials and
// accounts. The Anthropic directory runs until ctx ends.
//
// stop is called at shutdown before the processor closes: it waits for the
// lookups in flight and offers their rows, then stops the rows. What is
// learned after it (an account response the processor drains last) is
// dropped, never offered to a closed processor; the credential is learned
// again on its next sighting.
func setupIdentity(ctx context.Context, cfg identityConfig, instanceID string, offer func(events.Event)) (ids *identity.Cache, stop func()) {
	var dir *identity.Directory
	if cfg.adminKey != "" {
		dir = &identity.Directory{AdminKey: cfg.adminKey, Origin: cfg.adminOrigin, Every: cfg.directoryEvery}
		go dir.Run(ctx)
	}
	// Rows go through a buffer of their own: an observed response is learned
	// on the processor's goroutine, which must never wait on its own queue.
	rows := make(chan events.Event, 1024)
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for e := range rows {
			offer(e)
		}
	}()
	var mu sync.Mutex // guards stopped, and rows' closing
	stopped := false
	c := cfg.cache
	c.Resolvers = identity.Resolvers(dir)
	c.Learned = func(l identity.Learned) {
		if l.AccountID == "" {
			return // only an organization: no credential row (credentials name an account)
		}
		e := events.Event{Phase: events.PhaseCredential, TimestampNS: l.NS, ObserverInstance: instanceID,
			KeyHash: l.KeyHash, KeyHint: l.KeyHint, Credential: &events.Credential{AccountID: l.AccountID, Source: l.How}}
		if l.How == "lookup" { // an observed response's own row names the account already
			e.Auth = &events.AuthIdentity{AccountID: l.AccountID, Email: l.Email, Name: l.Name, Org: l.Org, OrgID: l.OrgID}
		}
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			slog.Debug("identity: a credential learned while shutting down: not recorded")
			return
		}
		select {
		case rows <- e:
		default:
			slog.Warn("identity: too many credentials learned at once; a credential row was dropped (written again when it's next learned)")
		}
	}
	ids = identity.New(c)
	stop = func() {
		ids.Close() // the lookups in flight end, and their rows are queued
		mu.Lock()
		if !stopped {
			stopped = true
			close(rows)
		}
		mu.Unlock()
		<-forwarded // and offered
	}
	if c.Origin != "" {
		slog.Warn("identity lookups go to a test origin, not the providers (OBSERVER_IDENTITY_TEST_ORIGIN): tests only", "origin", c.Origin)
	}
	slog.Info("identity", "lookup", c.Lookup, "providers", c.Providers, "max_wait", cfg.wait, "enforce_max_wait", cfg.enforceWait,
		"anthropic_directory", dir != nil)
	return ids, stop
}
