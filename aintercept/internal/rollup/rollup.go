package rollup

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// Config is a rollup replica's setup. Zero durations take the defaults.
type Config struct {
	Store   stream.Store
	Redis   *redis.Client
	Prefix  string       // the bucket prefix; v1/ is under it
	Replica string       // this replica's name (lock values)
	Model   stream.Model // stream.StateModel in production
	Clock   stream.Clock

	Grace      time.Duration // late-file window on in/ names (5 min)
	LockTTL    time.Duration // lock:<key> expiry (60 s)
	Renew      time.Duration // lock renewal period (20 s)
	Idle       time.Duration // release a key after this long with no input (60 s)
	Fanout     int           // output dirs (and state chunks) of one tier merged together (8)
	GCGrace    time.Duration // a superseded file is deleted after this (10 min)
	SweepEvery time.Duration // the sweep's period (10 min)
	SweepSpan  time.Duration // how far back the sweep reads recent/ (15 min)
	MaxFiles   int           // in/ files applied per run at most (500)
	Parallel   int           // keys run at once (8)

	// Hook, if set, is called at each step of a run ("listed", "read", "out",
	// "state", "commit", "committed"); an error stops the run there, as a crash
	// would. Tests only.
	Hook func(step, key string) error
}

// Worker is one rollup replica.
type Worker struct{ cfg Config }

// New is a replica.
func New(cfg Config) *Worker { return &Worker{cfg: cfg} }

var errTODO = errors.New("rollup: not implemented")

// RunKey runs key once under its lock: applies the key's new in/ files and
// commits. It returns how many files it applied and whether it committed.
func (w *Worker) RunKey(ctx context.Context, key string) (applied int, committed bool, err error) {
	return 0, false, errTODO
}

// Tick is one scheduling pass: claim dirty keys, run them, release idle ones.
func (w *Worker) Tick(ctx context.Context) error { return errTODO }

// Sweep re-flags every key in the recent index of the last SweepSpan.
func (w *Worker) Sweep(ctx context.Context) error { return errTODO }

// Lag is how long the oldest flagged key has waited.
func (w *Worker) Lag(ctx context.Context) (time.Duration, error) { return 0, errTODO }

// Release drops every lock this replica holds.
func (w *Worker) Release(ctx context.Context) {}

// Run loops Tick (every second) and Sweep until ctx ends, then releases.
func (w *Worker) Run(ctx context.Context) error { return errTODO }

// ReadNewest is key's newest commit (nil, nil: none yet).
func ReadNewest(ctx context.Context, s stream.Store, prefix, key string) (*Commit, error) {
	return nil, errTODO
}

// ReadTables is key's current rows per table, as readers see them: every
// output dir of the newest commit, oldest first, newest row per (key, id).
func ReadTables(ctx context.Context, s stream.Store, prefix, key string) (map[string][]stream.Row, error) {
	return nil, errTODO
}

// Handler serves /healthz and /lag.
func (w *Worker) Handler() http.Handler { return http.NotFoundHandler() }
