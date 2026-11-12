package events

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

// Processor is the telemetry path. The ext_proc handler calls Offer(event); a
// background goroutine drains the buffer into the Sink, whose writer spools to
// disk and never waits on the object store.
//
// Backpressure policy (docs/STREAMING_PIPELINE.md §1.4–1.5): nothing is
// dropped while the writer's spool has room. When the buffer is full, Offer
// waits for room, which pushes back through gRPC flow control instead of
// shedding data. Only once the spool is full (Full reports true) does Offer
// drop, as observe mode's counted last resort: observed traffic must never
// break. Enforced streams use OfferMust, which never drops: their calls are
// refused at the start instead while the spool is full.
type Processor struct {
	buf  chan Event
	sink Sink

	// schemaSink, when set, receives structure-only fingerprints of transactions
	// the extractors did not fully recognize (see SampleIfUnrecognized). It is a
	// separate sink so schema samples land under their own key prefix, apart from
	// normal telemetry. nil disables sampling.
	schemaSink Sink
	// schemaSampler rate/byte-limits sampling so a flood of a new shape cannot
	// fill the schema sink. nil (disabled) => sampling off.
	schemaSampler *sampleLimiter
	// clientShapesSeen dedups new client-event shapes already sampled. Only
	// touched by the drain goroutine.
	clientShapesSeen map[string]struct{}

	accepted        atomic.Uint64
	lastResortDrops atomic.Uint64
	emitted         atomic.Uint64
	emitErrs        atomic.Uint64
	schemaSampled   atomic.Uint64
	schemaCapHit    atomic.Uint64
	schemaEmitErrs  atomic.Uint64
	panics          atomic.Uint64

	// alarmEvery controls how often a sustained-drop summary is logged.
	alarmEvery uint64
	// full reports the writer's spool full (nil: never).
	full       func() bool
	budget     *membudget.Budget
	onComplete func(*Event)
	done       chan struct{}
}

// Config configures a Processor.
type ProcessorConfig struct {
	// BufferSize bounds in-flight events. A full buffer makes Offer wait.
	BufferSize int
	// AlarmEveryDrops logs a summary every N drops (in addition to the first).
	AlarmEveryDrops uint64

	// SchemaSink, when non-nil, receives structure-only fingerprints of
	// transactions the extractors did not fully recognize. Leave nil to disable
	// unrecognized-shape sampling.
	SchemaSink Sink
	// SchemaSamplesPerMinute caps how many samples per minute are emitted to
	// SchemaSink (per observer instance), so a flood of a new shape cannot fill
	// the schema sink. Default 120 when SchemaSink is set and this is zero.
	SchemaSamplesPerMinute int

	// Budget, when non-nil, is released by each event's Held bytes once the
	// event is processed (or dropped).
	Budget *membudget.Budget

	// OnComplete, when set, is called on the drain goroutine with each
	// complete event (and WebSocket connection row) once its metadata is
	// extracted: the Interceptor's OnCallComplete. It must not block.
	OnComplete func(*Event)

	// Full reports the writer's spool full; while it does, Offer drops (the
	// counted last resort). nil: never full.
	Full func() bool
}

// NewProcessor starts the drain goroutine and returns the processor.
func NewProcessor(sink Sink, cfg ProcessorConfig) *Processor {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 4096
	}
	if cfg.AlarmEveryDrops == 0 {
		cfg.AlarmEveryDrops = 100
	}
	p := &Processor{
		buf:        make(chan Event, cfg.BufferSize),
		sink:       sink,
		schemaSink: cfg.SchemaSink,

		clientShapesSeen: map[string]struct{}{},
		alarmEvery:       cfg.AlarmEveryDrops,
		budget:           cfg.Budget,
		onComplete:       cfg.OnComplete,
		full:             cfg.Full,
		done:             make(chan struct{}),
	}
	if cfg.SchemaSink != nil {
		perMin := cfg.SchemaSamplesPerMinute
		if perMin <= 0 {
			perMin = 120
		}
		p.schemaSampler = newSampleLimiter(perMin)
	}
	go p.drain()
	return p
}

// Offer enqueues an event from an observed stream. It waits while the buffer
// is full; it drops the event only while the writer's spool is full, counting
// it (observe mode's last resort). Returns false if it dropped.
func (p *Processor) Offer(e Event) bool {
	if p.full != nil && p.full() {
		p.budget.Add(-e.held)
		n := p.lastResortDrops.Add(1)
		if n == 1 || n%p.alarmEvery == 0 {
			slog.Error("ALARM spool full: dropping observed events as a last resort; real traffic unaffected",
				"last_resort_drops", n)
		}
		return false
	}
	p.buf <- e
	p.accepted.Add(1)
	return true
}

// OfferMust enqueues an event from an enforced stream. It never drops: it
// waits for room. (Enforced calls are refused at the start while the spool is
// full, so the calls already admitted are the ones that keep writing.)
func (p *Processor) OfferMust(e Event) {
	p.buf <- e
	p.accepted.Add(1)
}

// drain moves buffered events into the sink on a single goroutine.
func (p *Processor) drain() {
	defer close(p.done)
	for e := range p.buf {
		// Process each event under a recover so a panic on ONE poisoned transaction
		// (e.g. a pathological body that trips a parser) skips that event instead of
		// unwinding drain() — which would silently stop ALL telemetry while the
		// request path stayed up. Fail-open at the loop level, matching the
		// fingerprinter's own recover.
		p.processOne(e)
		p.budget.Add(-e.held)
	}
}

// processOne runs the off-hot-path work for a single event: deferred metadata
// extraction, unrecognized-shape sampling, buffer release, and the sink emit. A
// panic anywhere here is recovered and counted, so one bad event never kills the
// drain goroutine.
func (p *Processor) processOne(e Event) {
	raw := e // as it arrived, for a panic below
	defer func() {
		if r := recover(); r != nil {
			n := p.panics.Add(1)
			if n == 1 || n%p.alarmEvery == 0 {
				slog.Error("ALARM recovered panic while processing event; recording it unprocessed",
					"total_panics", n, "panic", r)
			}
			p.emitRaw(raw)
		}
	}()

	// Deferred metadata extraction (model/tokens/tools) runs HERE, off the
	// ext_proc serving goroutine, so the heavy JSON parse never adds latency
	// to the proxied request path. No-op for events without attached bodies.
	e.ExtractDeferred()
	if p.onComplete != nil && (e.Phase == PhaseComplete || e.Phase == PhaseWSConnection) {
		p.onComplete(&e)
	}
	// Unrecognized-shape sampling: if we did NOT fully recognize this
	// transaction, retain a structure-only fingerprint on the schema sink. We
	// do this while the raw buffers are still attached and BEFORE clearing
	// them. Rate-capped so a new shape can't flood the schema sink.
	// Synthetic (health-probe) traffic is skipped: its shape is known, and it
	// would otherwise spend the sampling budget meant for real unknown traffic.
	if p.schemaSink != nil && !e.Synthetic {
		if sample, ok := e.SampleIfUnrecognized(); ok {
			if p.schemaSampler.allow() {
				if err := p.schemaSink.Emit(sample); err != nil {
					n := p.schemaEmitErrs.Add(1)
					if n == 1 || n%p.alarmEvery == 0 {
						slog.Warn("ALARM schema sink emit error", "total", n, "err", err)
					}
				} else {
					p.schemaSampled.Add(1)
				}
			} else {
				p.schemaCapHit.Add(1)
			}
		}
	}
	// Client analytics events become rows of their own, emitted after the
	// transaction's row.
	clientRows := e.ClientEventRows()
	// Drop the raw buffers now that extraction and sampling are done — content
	// never leaves the process beyond the structure-only fingerprint above.
	e.clearBuffers()
	p.emit(e)
	for _, row := range clientRows {
		p.sampleNewClientEvent(row)
		p.emit(row)
	}
}

// emit sends one row to the sink. The writer only refuses a row once it's
// closed, which happens after the processor drains, so an error here is a bug
// that loses data: logged as such every time.
func (p *Processor) emit(e Event) {
	if err := p.sink.Emit(e); err != nil {
		p.emitErrs.Add(1)
		slog.Error("ALARM sink refused an event; it is NOT recorded", "correlation_id", e.CorrelationID, "phase", e.Phase, "err", err)
		return
	}
	p.emitted.Add(1)
}

// emitRaw records an event whose processing panicked, without its buffers and
// without the deferred extraction that failed.
func (p *Processor) emitRaw(e Event) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ALARM recovered panic recording an event unprocessed; it is NOT recorded", "panic", r)
		}
	}()
	e.clearBuffers()
	p.emit(e)
}

// Close stops accepting events, drains the buffer, and flushes the sink(s).
func (p *Processor) Close() {
	close(p.buf)
	<-p.done
	if err := p.sink.Flush(); err != nil {
		slog.Error("sink flush error", "err", err)
	}
	if p.schemaSink != nil {
		if err := p.schemaSink.Flush(); err != nil {
			slog.Error("schema sink flush error", "err", err)
		}
	}
}

// Stats is a snapshot of processor counters.
type Stats struct {
	Accepted        uint64
	LastResortDrops uint64
	Panics          uint64
	Emitted         uint64
	EmitErrs        uint64
	SchemaSampled   uint64 // unrecognized-shape fingerprints emitted
	SchemaCapHit    uint64 // samples suppressed by the per-minute cap
	Depth           int
	Capacity        int
}

// Stats returns a counter snapshot (for the /stats endpoint and periodic logs).
func (p *Processor) Stats() Stats {
	return Stats{
		Accepted:        p.accepted.Load(),
		LastResortDrops: p.lastResortDrops.Load(),
		Panics:          p.panics.Load(),
		Emitted:         p.emitted.Load(),
		EmitErrs:        p.emitErrs.Load(),
		SchemaSampled:   p.schemaSampled.Load(),
		SchemaCapHit:    p.schemaCapHit.Load(),
		Depth:           len(p.buf),
		Capacity:        cap(p.buf),
	}
}

// StartStatsLogger periodically logs counters so operators see drop/health
// trends without scraping. Returns a stop function.
func (p *Processor) StartStatsLogger(every time.Duration) func() {
	if every <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s := p.Stats()
				slog.Info("observer stats",
					"accepted", s.Accepted,
					"emitted", s.Emitted,
					"last_resort_drops", s.LastResortDrops,
					"emit_errs", s.EmitErrs,
					"schema_sampled", s.SchemaSampled,
					"schema_cap_hit", s.SchemaCapHit,
					"depth", s.Depth,
					"capacity", s.Capacity)
			}
		}
	}()
	return func() { close(stop) }
}

// maxClientShapesSeen bounds the dedup set of new client-event shapes; it is
// cleared when full, so a long-running replica re-reports occasionally rather
// than growing without bound.
const maxClientShapesSeen = 4096

// sampleNewClientEvent sends a structure-only sample for a client event the
// registry doesn't know, once per distinct new shape per replica.
func (p *Processor) sampleNewClientEvent(row Event) {
	ce := row.ClientEvent
	if p.schemaSink == nil || row.Synthetic || ce == nil || (!ce.NewEvent && len(ce.NewFields) == 0) {
		return
	}
	key := clientShapeKey(ce)
	if _, seen := p.clientShapesSeen[key]; seen {
		return
	}
	if len(p.clientShapesSeen) >= maxClientShapesSeen {
		p.clientShapesSeen = map[string]struct{}{}
	}
	p.clientShapesSeen[key] = struct{}{}
	if !p.schemaSampler.allow() {
		p.schemaCapHit.Add(1)
		return
	}
	if err := p.schemaSink.Emit(clientEventSample(row)); err != nil {
		n := p.schemaEmitErrs.Add(1)
		if n == 1 || n%p.alarmEvery == 0 {
			slog.Warn("ALARM schema sink emit error", "total", n, "err", err)
		}
		return
	}
	p.schemaSampled.Add(1)
}
