// Content rules from handoff §3.3. Every number the UI shows goes through here
// (ESLint bans toLocaleString/toFixed/Intl everywhere else).

/** Counts with thousands separators: `21,305`; abbreviated from 100k: `140.9M`, `384k`, `1.1M`. */
export function formatCount(n: number): string {
  if (Math.abs(n) >= 1_000_000_000) return `${trim(n / 1_000_000_000)}B`;
  if (Math.abs(n) >= 1_000_000) return `${trim(n / 1_000_000)}M`;
  if (Math.abs(n) >= 100_000) return `${Math.round(n / 1000)}k`;
  return n.toLocaleString("en-US");
}

const trim = (x: number) => x.toFixed(1).replace(/\.0$/, "");

/**
 * Durations (handoff §11A.9): `71 ms`; `4.8 s` under 10 s, `22 s` up to a minute;
 * then the two largest units, keeping a zero: `24m 0s`, `8h 20m`, `2d 0h`.
 */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 10_000) return `${trim(ms / 1000)} s`;
  if (ms < 60_000) return `${Math.round(ms / 1000)} s`;
  const s = Math.floor(ms / 1000);
  const d = Math.floor(s / 86_400);
  const h = Math.floor((s % 86_400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m ${s % 60}s`;
}

/** Coarse age for badges: minutes under an hour (`2m`), then `formatDuration` (`1h 5m`, `2d 0h`). */
export function formatAge(ms: number): string {
  return ms < 3_600_000 ? `${Math.floor(ms / 60_000)}m` : formatDuration(ms);
}

/** Bytes, decimal units (handoff §11A.9): `400 B`; `4.1 KB` under 10 KB; `412 KB` under 1 MB; then `37.2 MB`. */
export function formatBytes(n: number): string {
  if (n < 1000) return `${n} B`;
  if (n < 10_000) return `${trim(n / 1000)} KB`;
  if (n < 1_000_000) return `${Math.round(n / 1000)} KB`;
  const units = ["MB", "GB", "TB"];
  let v = n / 1_000_000;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${trim(v)} ${units[i]}`;
}

/** Tokens are formatted by scope (handoff §11B.3): a total, or one model call's own counts. */
export type TokenScope = "total" | "call";

/**
 * Tokens (handoff §11B.3). Under 1,000 always exact (never `0.9k`).
 * - `total` (prompt, session and person totals, compactions): `2.6k` to 9.9k, then whole k `39k`, `905k`; then `1.1M`.
 * - `call` (one model call): one decimal in k up to 1M, `1.2k`, `43.0k`; then `1.1M`.
 */
export function formatTokens(n: number, scope: TokenScope): string {
  if (Math.abs(n) < 1000) return String(Math.round(n));
  const k = n / 1000;
  const k1 = Math.abs(Number(k.toFixed(1)));
  if (scope === "call" || k1 < 10) {
    if (k1 < 1000) return `${k.toFixed(1)}k`;
  } else if (Math.round(Math.abs(k)) < 1000) return `${Math.round(k)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

// Exact values, for tooltips (handoff §6B: the cell shows the short format, the tooltip the exact one).

/** An exact count with thousands separators, never abbreviated: `43,012`. */
export const formatExact = (n: number): string => Math.round(n).toLocaleString("en-US");

/** Exact milliseconds: `9,312 ms`. */
export const formatExactMs = (ms: number): string => `${formatExact(ms)} ms`;

/** Exact bytes: `8,214 B`. */
export const formatExactBytes = (n: number): string => `${formatExact(n)} B`;

/** A percentage as given, with at most one decimal: `99.7%`, `41%`. */
export const formatPct = (p: number): string => `${trim(p)}%`;

const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad = (n: number, w = 2) => String(n).padStart(w, "0");

/** The viewer's time zone abbreviation for a date ("PST", or "GMT+1" where there is none). */
function zoneOf(d: Date): string {
  const part = new Intl.DateTimeFormat("en-US", { timeZoneName: "short" }).formatToParts(d).find((p) => p.type === "timeZoneName");
  return part?.value ?? "";
}

/** A full time in the viewer's zone, with ms: `Tue Nov 3 2026 · 10:42:16.204 PST` (§6B.6 time cell). */
export function formatFullTime(iso: string): string {
  const d = new Date(iso);
  const day = `${DAYS[d.getDay()]} ${MONTHS[d.getMonth()]} ${d.getDate()} ${d.getFullYear()}`;
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
  return `${day} · ${time} ${zoneOf(d)}`.trimEnd();
}

/** US dollars, cents shown: `$0.21`, `$41.20`. */
export const formatUsd = (usd: number): string => `$${usd.toFixed(2)}`;

/** An offset from a request's start, to the hundredth under 10 s: `+1.87 s`, `+1.9 s`, `+12 s`. */
export function formatOffset(ms: number): string {
  const sign = ms < 0 ? "−" : "+";
  const a = Math.abs(ms);
  return a >= 1000 && a < 10_000 ? `${sign}${Number((a / 1000).toFixed(2))} s` : `${sign}${formatDuration(a)}`;
}
