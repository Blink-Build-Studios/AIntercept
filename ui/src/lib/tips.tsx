// Cell tooltips (handoff §6B.6): the exact values behind a table cell's short format.
// Content only; the rows put it on their cells with <Tooltip>.
import type { Schemas } from "../api/types";
import { unknownValue, type TooltipContent, type TooltipRow } from "../components/Tooltip/Tooltip";
import { formatBytes, formatCount, formatDuration, formatExact, formatExactBytes, formatExactMs, formatFullTime, formatPct } from "./format";
import { formatDay, formatRange, formatTime } from "./time";

type TokenCount = Schemas["TokenCount"];
type Coverage = Schemas["Coverage"];
type Friction = Schemas["Friction"];
type RequestError = NonNullable<Schemas["RequestRow"]["error"]>;

const exactOr = (n: number | null, reason: string) => (n === null ? unknownValue(reason) : formatExact(n));

/** Time cell: the full date and time with ms, in the viewer's zone. */
export const timeTip = (iso: string): TooltipContent => ({ body: formatFullTime(iso) });

/** Status cell: `200 OK` and the stop reason; an error names itself, how far it got, and its retry. */
export function statusTip(status: number | "ERR" | null, statusText: string | null, stopReason: string | null, error: RequestError | null): TooltipContent | null {
  if (error) {
    const after = [
      error.afterMs === null ? null : formatDuration(error.afterMs),
      error.bytesReceived === null ? null : `${formatBytes(error.bytesReceived)} received`,
    ].filter((x) => x !== null);
    const rows: TooltipRow[] = [];
    if (after.length) rows.push({ key: "After", value: after.join(" · ") });
    if (error.retries)
      rows.push({ key: "Retried", value: `${error.retries}${error.retrySucceededAt ? ` · succeeded ${formatTime(error.retrySucceededAt, new Date(error.retrySucceededAt), true)}` : ""}` });
    return { title: error.message.charAt(0).toUpperCase() + error.message.slice(1), rows };
  }
  if (status === null) return null;
  const rows: TooltipRow[] = [{ key: "Status", value: [status, statusText].filter((x) => x !== null).join(" ") }];
  if (stopReason) rows.push({ key: "Stop", value: stopReason });
  return { rows };
}

/** Duration cell: total, first byte and streaming, in exact ms. */
export function durationTip(durationMs: number | null, ttftMs: number | null): TooltipContent | null {
  if (durationMs === null) return null;
  const rows: TooltipRow[] = [{ key: "Total", value: formatExactMs(durationMs) }];
  if (ttftMs !== null) {
    rows.push({ key: "First byte", value: formatExactMs(ttftMs) });
    rows.push({ key: "Streaming", value: formatExactMs(Math.max(0, durationMs - ttftMs)) });
  }
  return { rows };
}

/** Tokens: Input is the sum of the three rows under it (cache read, cache write, uncached). */
export function tokensTip(t: TokenCount): TooltipContent {
  const parts = [t.cacheRead, t.cacheWrite, t.input];
  const input = parts.every((p) => p === null) ? null : parts.reduce<number>((a, p) => a + (p ?? 0), 0);
  const ttl = t.cacheWrite5m && !t.cacheWrite1h ? " · 5m" : t.cacheWrite1h && !t.cacheWrite5m ? " · 1h" : "";
  const reason = t.accuracy === "missing" ? "no usage on this call" : "not reported";
  return {
    rows: [
      { key: "Input", value: exactOr(input, reason) },
      { key: "Cache read", value: exactOr(t.cacheRead, reason) },
      { key: "Cache write", value: t.cacheWrite === null ? unknownValue(reason) : `${formatExact(t.cacheWrite)}${ttl}` },
      { key: "Uncached", value: exactOr(t.input, reason) },
      { key: "Output", value: exactOr(t.output, reason) },
    ],
  };
}

/** Bytes cell: up and down in exact bytes, with the response's content encoding. */
export function bytesTip(up: number | null, down: number | null, encoding: string | null, reason: string): TooltipContent {
  return {
    rows: [
      { key: "Up", value: up === null ? unknownValue(reason) : formatExactBytes(up) },
      { key: "Down", value: down === null ? unknownValue(reason) : `${formatExactBytes(down)}${encoding ? ` · ${encoding}` : ""}` },
    ],
  };
}

/** Coverage cell: what each number means, and why calls were missed. */
export function coverageTip(c: Coverage): TooltipContent {
  const rows: TooltipRow[] = [{ key: "Captured", value: `${formatCount(c.captured)} by the proxy` }];
  rows.push({ key: "Reported", value: c.reported === null ? unknownValue("no analytics to compare (pre-parser)") : `${formatCount(c.reported)} by Claude Code` });
  // The fraction lives here since the cell shows only what was missed (§11G.C2).
  if (c.reported) rows.push({ key: "Captured share", value: formatPct((100 * c.captured) / c.reported) });
  const why = c.windows.find((w) => w.cause)?.cause;
  if (c.notCaptured > 0) rows.push({ key: "Why", value: why ?? unknownValue("reason not determined") });
  return { title: c.notCaptured > 0 ? `${formatCount(c.notCaptured)} calls not captured` : "All calls captured", rows };
}

/** Friction cell: one row per kind present. */
export function frictionTip(f: Pick<Friction, "frustrated" | "interrupted" | "destructive">): TooltipContent | null {
  const rows: TooltipRow[] = [];
  if (f.frustrated > 0) rows.push({ key: `↯ ${f.frustrated}`, value: "frustrated (user said so)" });
  if (f.interrupted > 0) rows.push({ key: `✕ ${f.interrupted}`, value: "interrupted by the user" });
  if (f.destructive > 0) rows.push({ key: `▲ ${f.destructive}`, value: "destructive commands", tone: "danger" });
  return rows.length ? { rows } : null;
}

/** A duration span in words with its exact ms, for in-row durations without a ttft. */
export const spanTip = (ms: number): TooltipContent => ({ rows: [{ key: "Total", value: formatExactMs(ms) }] });

/** The full text of a cell that may be cut off (`kind="truncate"`). */
export const fullText = (text: string): TooltipContent => ({ body: text });

// Header stats, tiles, chips and chrome (handoff §6B.7). The words live here; only the numbers come from the API.

/** HeaderStat "Model calls": captured, and the gap Claude Code reported. */
export function modelCallsTip(captured: number, notCaptured: number | null): TooltipContent {
  const rows: TooltipRow[] = [{ key: "Captured", value: `${formatCount(captured)} by the proxy` }];
  if (notCaptured === null) rows.push({ key: "Not captured", value: unknownValue("no analytics to compare") });
  else if (notCaptured > 0) rows.push({ key: "Not captured", value: `${formatCount(notCaptured)}: reported by Claude Code, no matching request-id`, tone: "danger" });
  return { title: "Model calls", rows, hint: notCaptured ? `Click +${formatCount(notCaptured)} to list them` : undefined };
}

/** StatTile "Cost": its source, and how many calls were estimated (row left out at 0). */
export function costTip(estimatedCalls: number): TooltipContent {
  const rows: TooltipRow[] = [{ key: "Source", value: "costUSD from Claude Code analytics" }];
  if (estimatedCalls > 0) rows.push({ key: "Estimated", value: `${formatCount(estimatedCalls)} ${estimatedCalls === 1 ? "call" : "calls"} had no analytics event` });
  return { title: "Cost", rows };
}

/** CoverageLine: how many windows, the longest, and why. */
export function coverageLineTip(c: Coverage, now?: Date, clickable = false): TooltipContent {
  const longest = [...c.windows].sort((a, b) => Date.parse(b.to) - Date.parse(b.from) - (Date.parse(a.to) - Date.parse(a.from)))[0];
  const why = c.windows.find((w) => w.cause)?.cause;
  const rows: TooltipRow[] = [];
  if (longest) rows.push({ key: "Windows", value: `${formatCount(c.windows.length)} · longest ${formatRange(longest.from, longest.to, now)}` });
  rows.push({ key: "Why", value: why ?? unknownValue("reason not determined") });
  return { title: `${formatCount(c.notCaptured)} calls not captured`, rows, hint: clickable ? "Click to list them" : undefined };
}

/** KeyValueList row: where the value comes from, and on how many calls (or in how many sessions) it was seen. */
export const sourceTip = (source: string, seen?: { n: number; of: number; per: "calls" | "sessions" }): TooltipContent => ({
  rows: [
    { key: "Source", value: source },
    ...(seen ? [{ key: "Seen", value: `${seen.per === "calls" ? "on" : "in"} ${formatCount(seen.n)} of ${formatCount(seen.of)} ${seen.per}` }] : []),
  ],
});

/** The NOT RETAINED chip. */
export const notRetainedTip: TooltipContent = {
  body: "Content not retained. AIntercept stores its shape (size, counts, types), not the text.",
  hint: "Click: About this data",
};

/** The posture chip: the permission mode and its share of calls. */
export const postureTip = (mode: string, pct: number | null): TooltipContent => ({
  rows: [
    { key: "Mode", value: mode },
    { key: "Share", value: pct === null ? unknownValue("no permission mode reported") : `${formatPct(pct)} of calls` },
  ],
});

/** The range chip. */
export const rangeTip = (ms: number): TooltipContent => ({ body: `Selected range · ${formatDuration(ms)}`, hint: "× or Esc clears it" });

/** LiveBadge: the last poll, the newest data, and the poll intervals. */
export function liveBadgeTip(lastPoll: string | null, lastDataAt: string | null, visibleS: number, hiddenS: number): TooltipContent {
  const at = (iso: string | null, reason: string) => (iso === null ? unknownValue(reason) : formatTime(iso, new Date(iso), true));
  return {
    rows: [
      { key: "Last poll", value: at(lastPoll, "not polled yet") },
      { key: "Newest data", value: at(lastDataAt, "no data in scope") },
      { key: "Interval", value: `${visibleS} s visible · ${hiddenS} s hidden` },
    ],
  };
}

/** AgentChip: the agent, its version, and when it was first seen. */
export function agentTip(name: string, version: string | null, firstSeenAt: string, devices: number): TooltipContent {
  return {
    title: name,
    rows: [
      { key: "Version", value: version ?? unknownValue("not reported") },
      { key: "First seen", value: `${formatDay(firstSeenAt)} · ${devices} ${devices === 1 ? "device" : "devices"}` },
    ],
  };
}
