package stream

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Config configures one processor replica.
type Config struct {
	Store      Store
	Layout     Layout
	Model      Model
	Replica    string
	BucketBits int
	// Cycle is how often an owned bucket looks for new records; Renew how often
	// leases are renewed; TTL how long an unrenewed lease stays live.
	Cycle, Renew, TTL time.Duration
	Clock             Clock
	Grace             time.Duration
	CacheBytes        int
	Log               *slog.Logger
}

func (c *Config) defaults() {
	if c.Cycle == 0 {
		c.Cycle = 5 * time.Second
	}
	if c.Renew == 0 {
		c.Renew = 10 * time.Second
	}
	if c.TTL == 0 {
		c.TTL = DefaultTTL
	}
	if c.Clock == nil {
		c.Clock = RealClock{}
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

// Processor is one replica, built the way the observer's services are: a lease
// worker and one worker per owned bucket, each a Run(ctx) loop over a ticker,
// joined by channels to a supervisor that starts and stops bucket workers as
// ownership changes, with an error channel and a WaitGroup for shutdown.
type Processor struct {
	cfg Config
	wg  sync.WaitGroup
}

func New(cfg Config) *Processor {
	cfg.defaults()
	return &Processor{cfg: cfg}
}

// Buckets is how many buckets there are.
func (p *Processor) Buckets() int { return 1 << p.cfg.BucketBits }

// Wait returns once Run's workers have all exited.
func (p *Processor) Wait() { p.wg.Wait() }

// Run owns buckets until ctx ends, then gives its leases back.
func (p *Processor) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	le := &Leaser{S: p.cfg.Store, L: p.cfg.Layout, Replica: p.cfg.Replica, Buckets: p.Buckets(), TTL: p.cfg.TTL}
	ownedCh := make(chan map[int]int64, 1)
	fencedCh := make(chan int, p.Buckets())
	errCh := make(chan error, 1)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.leaseLoop(ctx, le, ownedCh)
	}()

	type running struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	workers := map[int]*running{}
	stop := func(b int) {
		if r, ok := workers[b]; ok {
			r.cancel()
			<-r.done
			delete(workers, b)
		}
	}
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err = <-errCh:
			break loop
		case b := <-fencedCh:
			stop(b)
		case owned := <-ownedCh:
			for b := range workers {
				if _, ok := owned[b]; !ok {
					stop(b)
				}
			}
			for b, epoch := range owned {
				if _, ok := workers[b]; ok {
					continue
				}
				bctx, bcancel := context.WithCancel(ctx)
				r := &running{cancel: bcancel, done: make(chan struct{})}
				workers[b] = r
				w := &Bucket{S: p.cfg.Store, L: p.cfg.Layout, M: p.cfg.Model, B: b, Bits: p.cfg.BucketBits,
					Epoch: epoch, Replica: p.cfg.Replica, Clock: p.cfg.Clock, Grace: p.cfg.Grace, CacheBytes: p.cfg.CacheBytes}
				p.wg.Add(1)
				go func(b int) {
					defer p.wg.Done()
					defer close(r.done)
					if p.bucketLoop(bctx, w) {
						fencedCh <- b
					}
				}(b)
			}
		}
	}
	for b := range workers {
		stop(b)
	}
	cancel()
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	if rerr := le.ReleaseAll(rctx); rerr != nil {
		p.cfg.Log.Warn("stream: releasing leases", "err", rerr)
	}
	return err
}

func (p *Processor) leaseLoop(ctx context.Context, le *Leaser, out chan map[int]int64) {
	t := time.NewTicker(p.cfg.Renew)
	defer t.Stop()
	for {
		owned, err := le.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			p.cfg.Log.Warn("stream: lease round", "err", err)
		}
		select { // the newest view replaces an unread one
		case <-out:
		default:
		}
		select {
		case out <- owned:
		case <-ctx.Done():
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// bucketLoop cycles one bucket until ctx ends; true if it was fenced.
func (p *Processor) bucketLoop(ctx context.Context, w *Bucket) bool {
	t := time.NewTicker(p.cfg.Cycle)
	defer t.Stop()
	for {
		_, err := w.Cycle(ctx)
		switch {
		case errors.Is(err, ErrFenced):
			p.cfg.Log.Info("stream: fenced; giving up bucket", "bucket", w.B)
			return true
		case err != nil && ctx.Err() == nil:
			p.cfg.Log.Warn("stream: cycle", "bucket", w.B, "err", err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}
