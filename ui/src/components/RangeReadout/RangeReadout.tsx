import { formatCount, formatDuration } from "../../lib/format";
import { Button } from "../Button/Button";
import "./RangeReadout.css";

export type RangeCounts = { prompts: number; modelCalls: number; events: number; requests?: number; compactions?: number };

export type RangeReadoutProps = {
  mode: "range" | "live" | "empty";
  range: { from: string; to: string };
  /** null while the range's counts are loading ("…", §6A.4). requests and compactions show when given. */
  counts?: RangeCounts | null;
  onPrev: () => void;
  onNext: () => void;
  onClear: () => void;
  /** The clear button's words (§11G.A8): "Clear range" (person page) or "Whole session" (session page). */
  clearLabel?: "Clear range" | "Whole session";
  nearest?: { earlier?: string; later?: string };
};

const pad = (n: number) => String(n).padStart(2, "0");
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const HOUR = 3_600_000;

/** The range's length: "15 min" under an hour, then formatDuration ("2h 0m", "6h 0m"; handoff §11A.9). */
export function formatSpan(ms: number): string {
  if (ms < HOUR) return `${Math.max(1, Math.round(ms / 60_000))} min`;
  return formatDuration(ms);
}

/**
 * "Nov 3 10:00:00 → 12:00:00 · 2h 0m": the day once, the end's day only when it
 * differs; seconds for ranges under 4h (brushed at a fine scale), minutes above.
 */
export function formatReadoutRange(from: string, to: string): string {
  const a = new Date(from);
  const b = new Date(to);
  const span = b.getTime() - a.getTime();
  const t = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}${span < 4 * HOUR ? `:${pad(d.getSeconds())}` : ""}`;
  const day = (d: Date) => `${MONTHS[d.getMonth()]} ${d.getDate()}`;
  const end = a.toDateString() === b.toDateString() ? t(b) : `${day(b)} ${t(b)}`;
  return `${day(a)} ${t(a)} → ${end} · ${formatSpan(span)}`;
}

/** The strip under the timeline naming the brushed range and what's in it (handoff §6.4). */
export function RangeReadout({ mode, range, counts, onPrev, onNext, onClear, clearLabel = "Whole session", nearest }: RangeReadoutProps) {
  const c = (n: number) => (counts ? formatCount(n) : "…");
  const span = Date.parse(range.to) - Date.parse(range.from);
  const items: string[] = [];
  if (mode !== "empty") {
    items.push(`${c(counts?.prompts ?? 0)} prompts`, `${c(counts?.modelCalls ?? 0)} model calls`);
    if (counts?.requests !== undefined) items.push(`${c(counts.requests)} requests`);
    items.push(`${c(counts?.events ?? 0)} events`);
    if (counts?.compactions) items.push(`${c(counts.compactions)} compactions`);
  }
  return (
    <div className="rr border border-focus bg-surface px-3 py-2">
      <span className="rr-key">Range</span>
      <span className="type-data-strong">{mode === "live" ? `Last ${formatSpan(span)} · following live` : formatReadoutRange(range.from, range.to)}</span>
      {mode === "empty" ? (
        <span className="type-data text-ink-muted">no activity</span>
      ) : (
        items.map((t) => (
          <span key={t.split(" ").slice(1).join(" ")} className="type-data" aria-live="polite">
            {t}
          </span>
        ))
      )}
      <span className="flex-1" />
      {mode === "empty" ? (
        <>
          {nearest?.earlier && <Button onClick={onPrev}>← nearest earlier</Button>}
          {nearest?.later && <Button onClick={onNext}>nearest later →</Button>}
          <Button onClick={onClear}>{`× ${clearLabel}`}</Button>
        </>
      ) : (
        <>
          <Button aria-label="Previous range" onClick={onPrev}>
            ‹
          </Button>
          <Button aria-label="Next range" onClick={onNext} disabled={mode === "live"} disabledReason="Following live: no later range">
            ›
          </Button>
          <Button onClick={onClear}>{`× ${clearLabel}`}</Button>
        </>
      )}
    </div>
  );
}
