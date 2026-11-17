import { formatDuration } from "../../lib/format";
import "./TimingWaterfall.css";

/** A proxy phase, from the request's start; null times: the proxy doesn't record it. */
export type TimingPhase = { label: string; startMs: number | null; durMs: number | null };

/** Waiting on the network is neutral; the response itself (first byte, streaming) is the model's color; the total bar is a hairline fill. */
const tone = (p: TimingPhase) => (/^total/.test(p.label) ? "bg-line" : /^(first byte|streaming)/.test(p.label) ? "bg-ch-model" : "bg-line-strong");
/** The narrowest bar, in % of the total, so a 4 ms phase still shows. */
const MIN_PCT = 1;

/**
 * The proxy's waterfall (handoff §6.6, §11D), to scale: each phase at its real
 * start and length on the total's scale (at least 1% wide, never pushing the
 * next along). A phase the proxy doesn't record keeps its row, with no bar.
 */
export function TimingWaterfall({ phases, className = "" }: { phases: TimingPhase[]; className?: string }) {
  const total = Math.max(1, ...phases.map((p) => (p.startMs ?? 0) + (p.durMs ?? 0)));
  return (
    <div className={`timing ${className}`}>
      {phases.map((p) => (
        <div key={p.label} className="timing-phase type-data">
          <span className="timing-label text-ink-muted">{p.label}</span>
          <span className="timing-track">
            {p.startMs === null || p.durMs === null ? (
              <span className="timing-unrecorded text-ink-faint">—  not recorded by the proxy</span>
            ) : (
              <span
                className={`timing-bar ${tone(p)}`}
                style={{
                  left: `${(p.startMs / total) * 100}%`,
                  width: `${Math.max((p.durMs / total) * 100, MIN_PCT)}%`,
                }}
              />
            )}
          </span>
          <span className="timing-value text-ink">{p.durMs === null ? "—" : formatDuration(p.durMs)}</span>
        </div>
      ))}
    </div>
  );
}
