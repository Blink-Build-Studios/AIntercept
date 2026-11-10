// Track props for the session page's lanes (handoff §5.2.8, §7.3, Session.dc.html),
// from `GET /sessions/{id}/timeline`. Pure: the page passes the response's `data`
// and lays the lanes out; stories feed it the generated samples.
import type { StateRow } from "./RowTrack";
import type { SessionTimeline } from "../../api/types";
import { formatCount, formatDuration, formatTokens } from "../../lib/format";
import type { Bin, Gap } from "./BarTrack";
import type { MarkLabel, TimelineMark } from "./MarkerTrack";
import { ms } from "./scale";
import type { Segment, StateChange } from "./StateTrack";

/** The bin sublabel, naming the bin (§11G.A5): "per 15m", "per 1h", "per 1d". */
export function perBin(binSeconds: number): string {
  if (binSeconds % 86_400 === 0) return `per ${formatCount(binSeconds / 86_400)}d`;
  if (binSeconds % 3600 === 0) return `per ${formatCount(binSeconds / 3600)}h`;
  return `per ${formatCount(binSeconds / 60)}m`;
}

/** The header's bin size: "30-min bins", "1-hour bins". */
export function binsLabel(binSeconds: number): string {
  return binSeconds % 3600 === 0 ? `${formatCount(binSeconds / 3600)}-hour bins` : `${formatCount(binSeconds / 60)}-min bins`;
}

/**
 * Prompt labels ("#13") on the first prompt of each burst: a prompt is labelled
 * when it starts at least `minGap` (a fraction of the window) after the last label.
 */
export function promptLabels(prompts: { t: string; index: number | null }[], from: string, to: string, minGap = 0.08): MarkLabel[] {
  const span = Math.max(1, ms(to) - ms(from));
  const out: MarkLabel[] = [];
  let last = -Infinity;
  for (const p of prompts) {
    const at = ms(p.t);
    if (p.index === null || (at - last) / span < minGap) continue;
    out.push({ t: p.t, text: `#${p.index}` });
    last = at;
  }
  return out;
}

/**
 * Context per bin (input + cacheRead + cacheWrite of the main thread): the last
 * call's context in each bin, carried forward through bins without calls (the
 * context is still held) until the window's end, as on the Session sheet.
 */
export function contextBins(context: { t: string; tokens: number }[], binSeconds: number, to: string): Bin[] {
  const first = context[0];
  if (!first) return [];
  const binMs = binSeconds * 1000;
  const floor = (t: number) => Math.floor(t / binMs) * binMs;
  const last = new Map<number, number>();
  for (const c of [...context].sort((a, b) => ms(a.t) - ms(b.t))) last.set(floor(ms(c.t)), c.tokens);
  const out: Bin[] = [];
  let v = 0;
  for (let b = floor(Math.min(...context.map((c) => ms(c.t)))); b < ms(to); b += binMs) {
    v = last.get(b) ?? v;
    out.push({ t: new Date(b).toISOString(), v });
  }
  return out;
}

/** "⇣ compaction 903k → 64,133 · 1m 11s"; idle ones say "idle compaction". */
export function compactionLabels(compactions: SessionTimeline["compactions"]): MarkLabel[] {
  return compactions.map((c) => {
    const tokens = [c.from, c.to].filter((n): n is number => n !== null).map((n) => formatTokens(n, "total")).join(" → ");
    const wait = c.waitMs === null ? "" : ` · ${formatDuration(c.waitMs)}`;
    return { t: c.t, text: `⇣ ${c.idle ? "idle compaction" : "compaction"}${tokens ? ` ${tokens}` : ""}${wait}` };
  });
}

/**
 * Permission segments; a run of segments too brief to read (under `minSpan`
 * of the window) folds into the segment before it and becomes a focus tick:
 * "→ default ×2 in 22 s".
 */
export function permissionLane(permission: SessionTimeline["permission"], from: string, to: string, minSpan = 0.005): { segments: Segment[]; changes: StateChange[] } {
  const span = Math.max(1, ms(to) - ms(from));
  const brief = (p: { from: string; to: string }) => (ms(p.to) - ms(p.from)) / span < minSpan;
  const segments: Segment[] = [];
  const changes: StateChange[] = [];
  let run: SessionTimeline["permission"] = [];
  const flush = () => {
    const head = run[0];
    const tail = run.at(-1);
    if (!head || !tail) return;
    const prev = segments.at(-1);
    const away = run.filter((r) => r.mode !== prev?.state);
    const counts = new Map<string, number>();
    for (const r of away) counts.set(r.mode, (counts.get(r.mode) ?? 0) + 1);
    const [mode, n] = [...counts].sort((a, b) => b[1] - a[1])[0] ?? [head.mode, run.length];
    const runFrom = head.from;
    const runTo = tail.to;
    changes.push({ t: runFrom, to: runTo, label: `→ ${mode}${n > 1 ? ` ×${n}` : ""} in ${formatDuration(ms(runTo) - ms(runFrom))}` });
    if (prev) prev.to = runTo;
    run = [];
  };
  for (const p of permission) {
    if (brief(p) && segments.length) {
      run.push(p);
      continue;
    }
    flush();
    const prev = segments.at(-1);
    if (prev && prev.state === p.mode && prev.to === p.from) prev.to = p.to;
    else segments.push({ from: p.from, to: p.to, state: p.mode });
  }
  flush();
  return { segments, changes };
}

/**
 * Presence holds this terminal's focus and the device's sleep together; the
 * session page splits it: Focus = focused / away ("blurred 3h 30m"), Device
 * sleep = asleep ("asleep 7h 0m · clock jump").
 */
export function presenceLanes(presence: SessionTimeline["presence"]): { focus: Segment[]; sleep: Segment[] } {
  const span = (p: { from: string; to: string }) => formatDuration(ms(p.to) - ms(p.from));
  const focus: Segment[] = [];
  const sleep: Segment[] = [];
  for (const p of presence) {
    const seg = { from: p.from, to: p.to, state: p.state, label: p.label };
    if (p.state === "asleep") sleep.push({ ...seg, text: `asleep ${span(p)}${p.label ? ` · ${p.label}` : ""}` });
    else focus.push({ ...seg, text: p.state === "away" ? `blurred ${span(p)}` : "focused" });
  }
  return { focus, sleep };
}

/**
 * Network segments named by first appearance ("net A · 192.0.2.10 · IPv6"). A
 * segment that a not-captured window overlaps is `bypassed` (danger text).
 */
export function networkSegments(network: SessionTimeline["network"]): Segment[] {
  const names = new Map<string, string>();
  return network.map((n) => {
    if (!names.has(n.label)) names.set(n.label, `net ${String.fromCharCode(65 + (names.size % 26))}`);
    const tail = n.bypassed ? (n.ipv6 ? " · has IPv6 → bypassed proxy" : " → bypassed proxy") : n.ipv6 ? " · IPv6" : "";
    return { from: n.from, to: n.to, state: n.bypassed ? "bypassed" : "ok", label: n.label, text: `${names.get(n.label)} · ${n.label}${tail}` };
  });
}

/** Every session lane's track props from one `/sessions/{id}/timeline` response. */
export function sessionLanes(d: SessionTimeline) {
  const ctx = contextBins(d.context, d.binSeconds, d.to);
  const permission = permissionLane(d.permission, d.from, d.to);
  const { focus, sleep } = presenceLanes(d.presence);
  return {
    window: { from: d.from, to: d.to, now: d.now },
    binSeconds: d.binSeconds,
    perBin: perBin(d.binSeconds),
    binsLabel: binsLabel(d.binSeconds),
    prompts: { markers: d.prompts.map((p): TimelineMark => ({ t: p.t, id: p.promptId, hoverLabel: p.index === null ? undefined : `#${p.index}` })), labels: promptLabels(d.prompts, d.from, d.to) },
    modelCalls: { bins: d.modelCalls.map((b): Bin => ({ t: b.t, v: b.count })), gaps: d.notCaptured.map((g): Gap => ({ from: g.from, to: g.to, count: g.count })) },
    context: { bins: ctx, labels: compactionLabels(d.compactions), peak: Math.max(0, ...ctx.map((b) => b.v)) },
    permission,
    // Rows (§11G.A3): focus = focused and blurred; device = asleep.
    focusRows: [
      { name: "focused", tone: "ink", segments: focus.filter((s) => s.state === "focused") },
      { name: "blurred", tone: "muted", segments: focus.filter((s) => s.state !== "focused") },
    ] satisfies StateRow[],
    sleepRows: [{ name: "asleep", tone: "faint", segments: sleep }] satisfies StateRow[],
    network: networkSegments(d.network),
    friction: d.friction,
  };
}
export type SessionLanes = ReturnType<typeof sessionLanes>;
