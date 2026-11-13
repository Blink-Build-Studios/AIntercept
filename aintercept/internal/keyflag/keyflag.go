// Package keyflag tells the rollups which keys have new ingest files
// (docs/PIPELINE.md §1): after each file lands, SADD dirty <key> in Redis
// (fire-and-forget), the key into this pod's per-minute recent index, and the
// pod's newest ingest time into Redis for /status.
package keyflag

import "time"

// Config configures a Flagger.
type Config struct {
	Redis       string // host:port; empty: no Redis (local development)
	Prefix      string
	Incarnation string
	Put         func(key string, body []byte) error // uploads the recent index
	Now         func() time.Time
}

// Flagger flags keys.
type Flagger struct{ cfg Config }

// New starts a Flagger.
func New(cfg Config) *Flagger { return &Flagger{cfg: cfg} }

// InTemplate is an ingest file's key template (rollup.InPrefix + InName, with
// the writer's UploadTime placeholder for the time).
func InTemplate(prefix, key, incarnation string, seq int64) string { return "" }

// Landed is the writer's OnUploaded hook.
func (f *Flagger) Landed(target, objKey string) {}

// FlushRecent uploads the recent index's pending minutes.
func (f *Flagger) FlushRecent() error { return nil }

// Wait waits for queued Redis writes (tests).
func (f *Flagger) Wait() {}

// Close stops the Flagger.
func (f *Flagger) Close() {}
