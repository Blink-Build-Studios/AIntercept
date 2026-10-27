package events

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// Processor is the non-blocking telemetry path. The ext_proc handler calls
// Offer(event) and returns to Envoy immediately; a background goroutine drains
// the buffer into the Sink. This is the "buffer-and-ack-immediately, process
// separately" design: the request path never waits on event processing or on
// the sink.
//
// Backpressure policy: the buffer is bounded. If it is full, Offer DROPS the
// event, increments a dropped counter, and raises an alarm (logged loudly, and
// periodically summarized). Dropping is deliberate — losing telemetry is always
// preferable to adding latency to, or stalling, real API traffic.
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

	accepted       atomic.Uint64
	dropped        atomic.Uint64
	emitted        atomic.Uint64
	emitErrs       atomic.Uint64
	schemaSampled  atomic.Uint64
	schemaCapHit   atomic.Uint64
	schemaEmitErrs atomic.Uint64
	panics         atomic.Uint64

	// alarmEvery controls how often a sustained-drop summary is logged.
	alarmEvery uint64
	done       chan struct{}
}

// Config configures a Processor.
type ProcessorConfig struct {
	// BufferSize bounds in-flight events. Full buffer => drop + alarm.
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
		alarmEvery: cfg.AlarmEveryDrops,
		done:       make(chan struct{}),
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

// Offer enqueues an event without blocking. Returns false if the event was
// dropped because the buffer was full. The caller (ext_proc handler) ignores
// the return and acks Envoy regardless — the bool exists for testing and
// metrics.
func (p *Processor) Offer(e Event) bool {
	select {
	case p.buf <- e:
		p.accepted.Add(1)
		return true
	default:
		n := p.dropped.Add(1)
		// Alarm: first drop is always loud; thereafter every alarmEvery drops.
		if n == 1 || n%p.alarmEvery == 0 {
			slog.Warn("ALARM telemetry buffer full — dropping events; real traffic unaffected, telemetry being shed",
				"total_dropped", n, "buffer", cap(p.buf))
		}
		return false
	}
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
	}
}

// processOne runs the off-hot-path work for a single event: deferred metadata
// extraction, unrecognized-shape sampling, buffer release, and the sink emit. A
// panic anywhere here is recovered and counted, so one bad event never kills the
// drain goroutine.
func (p *Processor) processOne(e Event) {
	defer func() {
		if r := recover(); r != nil {
			n := p.panics.Add(1)
			if n == 1 || n%p.alarmEvery == 0 {
				slog.Error("ALARM recovered panic while processing event — event dropped, drain goroutine survives",
					"total_panics", n, "panic", r)
			}
		}
	}()

	// Deferred metadata extraction (model/tokens/tools) runs HERE, off the
	// ext_proc serving goroutine, so the heavy JSON parse never adds latency
	// to the proxied request path. No-op for events without attached bodies.
	e.ExtractDeferred()
	// Unrecognized-shape sampling: if we did NOT fully recognize this
	// transaction, retain a structure-only fingerprint on the schema sink. We
	// do this while the raw buffers are still attached and BEFORE clearing
	// them. Rate-capped so a new shape can't flood the schema sink.
	if p.schemaSink != nil {
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
	// Drop the raw buffers now that extraction and sampling are done — content
	// never leaves the process beyond the structure-only fingerprint above.
	e.clearBuffers()
	if err := p.sink.Emit(e); err != nil {
		n := p.emitErrs.Add(1)
		if n == 1 || n%p.alarmEvery == 0 {
			slog.Warn("ALARM sink emit error", "total", n, "err", err)
		}
		return
	}
	p.emitted.Add(1)
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
	Accepted      uint64
	Dropped       uint64
	Emitted       uint64
	EmitErrs      uint64
	SchemaSampled uint64 // unrecognized-shape fingerprints emitted
	SchemaCapHit  uint64 // samples suppressed by the per-minute cap
	Depth         int
	Capacity      int
}

// Stats returns a counter snapshot (for the /stats endpoint and periodic logs).
func (p *Processor) Stats() Stats {
	return Stats{
		Accepted:      p.accepted.Load(),
		Dropped:       p.dropped.Load(),
		Emitted:       p.emitted.Load(),
		EmitErrs:      p.emitErrs.Load(),
		SchemaSampled: p.schemaSampled.Load(),
		SchemaCapHit:  p.schemaCapHit.Load(),
		Depth:         len(p.buf),
		Capacity:      cap(p.buf),
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
					"dropped", s.Dropped,
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
