// Timeline tooltip copy (handoff §6B.4), built from the (6B) fields of the
// timeline payloads: tooltips never fetch (§6B.10). Exact copy per mark.
import type { Schemas } from "../../api/types";
import { formatCount, formatDuration, formatExact, formatTokens } from "../../lib/format";
import { formatDayTime, formatTime } from "../../lib/time";
import { unknownValue, type TooltipContent, type TooltipRow } from "../Tooltip/Tooltip";
import { ms } from "./scale";

type S = Schemas;

const hm = (iso: string) => formatDayTime(iso).slice(-5);
const sameDay = (a: string, b: string) => new Date(a).toDateString() === new Date(b).toDateString();

/** "Nov 3 10:00–10:30" (a bin), naming the day once. */
export const dayRange = (from: string, to: string) => `${formatDayTime(from)}–${sameDay(from, to) ? hm(to) : formatDayTime(to)}`;

/** "Nov 3 12:40 → 14:20 · 1h 40m" (a span); "→ now" while live. */
export function span(from: string, to: string | null, now?: string): string {
  const end = to ?? now ?? from;
  const tail = to === null ? "now" : sameDay(from, to) ? hm(to) : formatDayTime(to);
  return `${formatDayTime(from)} → ${tail} · ${formatDuration(ms(end) - ms(from))}`;
}

/** The bin length in words, for the hint: "half hour", "hour", "15 minutes", "day". */
export function binWords(binSeconds: number): string {
  if (binSeconds === 1800) return "half hour";
  if (binSeconds === 3600) return "hour";
  if (binSeconds === 86_400) return "day";
  return binSeconds % 3600 === 0 ? `${formatCount(binSeconds / 3600)} hours` : `${formatCount(binSeconds / 60)} minutes`;
}

const tokensInOut = (i: number | null, o: number | null) =>
  i === null && o === null ? unknownValue("no usage on these calls") : `${i === null ? "—" : formatTokens(i, "total")} in → ${o === null ? "—" : formatTokens(o, "total")} out`;

/** A model-call bin (§6B.4): count, class split, tokens, not captured. */
export function modelBinTip(from: string, to: string, bin: S["ModelCallBin"] | undefined, binSeconds: number): TooltipContent {
  const rows: TooltipRow[] = [{ key: "Model calls", value: formatExact(bin?.count ?? 0) }];
  if (bin && bin.count > 0) {
    const c = bin.byClass;
    rows.push({ key: "", value: `main ${formatCount(c.main)} · subagent ${formatCount(c.subagent)} · auxiliary ${formatCount(c.auxiliary)}` });
    rows.push({ key: "Tokens", value: tokensInOut(bin.tokensIn, bin.tokensOut) });
  }
  const missed = bin?.notCaptured ?? 0;
  rows.push(missed > 0 ? { key: "Not captured", value: `▲ ${formatExact(missed)}`, tone: "danger" } : { key: "Not captured", value: "0" });
  // An empty bin answers too (§11H.2: Tooltip/emptyBin), without the drag hint.
  const hint = bin && bin.count > 0 ? `Click to select this ${binWords(binSeconds)} · drag to select a range` : `Click to select this ${binWords(binSeconds)}`;
  return { title: dayRange(from, to), rows, hint };
}

/** An analytics bin: the event count and its three most frequent names. */
export function analyticsBinTip(from: string, to: string, bin: S["AnalyticsBin"] | undefined, binSeconds: number): TooltipContent {
  const rows: TooltipRow[] = [{ key: "Events", value: formatExact(bin?.count ?? 0) }];
  bin?.top.forEach((e, i) => rows.push({ key: i === 0 ? "Top" : "", value: `${e.name} ${formatExact(e.count)}` }));
  return { title: dayRange(from, to), rows, hint: `Click to select this ${binWords(binSeconds)}` };
}

/** A not-captured window. */
export function gapTip(g: S["GapWindow"]): TooltipContent {
  return {
    title: `▲ ${formatExact(g.count)} calls not captured`,
    rows: [
      { key: "Window", value: `${dayRange(g.from, g.to)} · ${formatDuration(ms(g.to) - ms(g.from))}` },
      { key: "Reported", value: `${formatExact(g.reported)} by Claude Code` },
      { key: "Captured", value: `${formatExact(g.captured)} by the proxy` },
      { key: "Why", value: g.cause ?? unknownValue("reason not determined") },
    ],
    hint: "Click to list these calls (Requests · not captured)",
  };
}

/** Up to 8 rows of a merged or clustered mark's members, then `+N more` (§11G.A1, A4). */
const listed = <T,>(items: T[], row: (x: T) => TooltipRow): TooltipRow[] =>
  items.length > 8 ? [...items.slice(0, 8).map(row), { key: "", value: `+${items.length - 8} more` }] : items.map(row);

/** Gaps merged on screen (§11G.A4): the total, then each window (start · count · length). */
export function mergedGapTip(parts: { from: string; to: string; count: number }[]): TooltipContent {
  const total = parts.reduce((a, g) => a + g.count, 0);
  return {
    title: `▲ ${formatExact(total)} calls not captured · ${formatExact(parts.length)} windows`,
    rows: listed(parts, (g) => ({ key: formatDayTime(g.from), value: `${formatExact(g.count)} · ${formatDuration(ms(g.to) - ms(g.from))}` })),
    hint: "Click to list these calls",
  };
}

/** A session band (person page). */
export function sessionBandTip(s: S["TimelineSession"], now?: string): TooltipContent {
  const repo = s.repo === null ? unknownValue("not extracted yet") : s.branch ? `${s.repo} · ${s.branch}` : s.repo;
  const prompts = s.prompts === null ? "—" : formatExact(s.prompts);
  return {
    title: s.label,
    rows: [
      { key: "Repo", value: repo },
      { key: "Span", value: span(s.start, s.end, now) },
      { key: "Prompts", value: `${prompts} · ${formatExact(s.modelCalls)} calls` },
      s.live ? { key: "Status", value: "● live", tone: "live" } : { key: "Status", value: "ended" },
    ],
    hint: "Click to open the session",
  };
}

/** A context bar (session page): one main-thread call's context. */
export function contextTip(c: S["ContextPoint"]): TooltipContent {
  const pct = c.windowTokens ? ` · ${Math.round((c.tokens / c.windowTokens) * 100)}% of ${formatTokens(c.windowTokens, "total")}` : "";
  return {
    title: `Call ${formatTime(c.t, new Date(c.t), true)}${c.model ? ` · ${c.model}` : ""}`,
    rows: [
      { key: "Context", value: `${formatTokens(c.tokens, "call")} tokens${pct}` },
      { key: "Thread", value: c.thread },
      { key: "Prompt", value: c.promptIndex === null ? unknownValue("outside any prompt") : `#${c.promptIndex}` },
    ],
    hint: "Click to open this request",
  };
}

/** A compaction label. */
export function compactionTip(c: S["Compaction"]): TooltipContent {
  const tokens = c.from !== null && c.to !== null ? `${formatTokens(c.from, "total")} → ${formatTokens(c.to, "total")}` : unknownValue("no call on both sides");
  return {
    title: `${c.idle ? "Idle compaction" : "Compaction"} · ${formatDayTime(c.t)}`,
    rows: [
      { key: "Tokens", value: tokens },
      { key: "Messages", value: c.messages === null ? unknownValue("not extracted yet") : `${formatExact(c.messages)} summarized` },
      { key: "Wait", value: c.waitMs === null ? unknownValue("no attempt event") : formatDuration(c.waitMs) },
    ],
    hint: "Click to jump to it in Prompts",
  };
}

const STATE = { focused: "Focused", away: "Away", asleep: "Asleep" } as const;

/** A presence segment: state, span, where it comes from. */
export function presenceTip(p: S["PresenceSegment"]): TooltipContent {
  return {
    title: STATE[p.state],
    rows: [
      { key: "Span", value: span(p.from, p.to) },
      { key: "Source", value: p.source === "sleep" ? "device sleep · this device" : "focus events · this session" },
    ],
  };
}

/** Brief permission changes, one row each. */
export function permissionTip(changes: S["PermissionChange"][]): TooltipContent {
  const first = changes[0];
  const last = changes[changes.length - 1];
  const within = first && last ? formatDuration(ms(last.t) - ms(first.t)) : "";
  return {
    title: `Permission changed ×${changes.length}${changes.length > 1 ? ` in ${within}` : ""}`,
    rows: changes.map((c) => ({ key: formatTime(c.t, new Date(c.t), true), value: `${c.from} → ${c.to}` })),
    hint: changes.length > 1 ? `Click to select these ${within}` : undefined,
  };
}

/** A network segment; `name` is its lane name ("net 2"). */
export function networkTip(n: S["NetworkSegment"], name: string): TooltipContent {
  return {
    title: `${name} · ${n.ipv6 ? "IPv6" : "IPv4"}`,
    rows: [
      { key: "Span", value: span(n.from, n.to) },
      { key: "Address", value: n.address ?? unknownValue("not recorded yet") },
      n.bypassed
        ? { key: "Proxy", value: `▲ bypassed · ${formatExact(n.notCaptured)} calls not captured`, tone: "danger" }
        : { key: "Proxy", value: "via proxy" },
    ],
    hint: "Click to select this span",
  };
}

/** A prompt tick: its text when retained, else its shape. */
export function promptTip(p: S["TimelinePrompt"]): TooltipContent {
  const n = p.index === null ? "Prompt" : `#${p.index}`;
  const text = p.text.retained && p.text.text !== null ? `“${p.text.text}”` : p.text.chars === null ? "not retained" : `${formatExact(p.text.chars)} chars · not retained`;
  return {
    title: `${n} · ${formatDayTime(p.t)}`,
    rows: [
      { key: "Prompt", value: text },
      { key: "Steps", value: `${formatExact(p.steps)}${p.durationMs === null ? "" : ` · ${formatDuration(p.durationMs)}`}` },
      { key: "Tokens", value: p.tokensIn === null && p.tokensOut === null ? unknownValue("no captured model calls") : `${p.tokensIn === null ? "—" : formatTokens(p.tokensIn, "total")} → ${p.tokensOut === null ? "—" : formatTokens(p.tokensOut, "total")}` },
    ],
    hint: p.index === null ? "Click to jump to it in Prompts" : `Click to jump to #${p.index} in Prompts`,
  };
}

const FRICTION = {
  destructive: { title: "▲ Destructive command", detail: "Command" },
  frustrated: { title: "↯ Frustrated", detail: "Said" },
  interrupted: { title: "✕ Interrupted", detail: "Tool run" },
  steered: { title: "↻ Steered", detail: "Steer" },
} as const;
/** A friction mark: the API's kinds and Codex's own `steered` (§11J). */
type FrictionMarkLike = Omit<S["FrictionMark"], "kind"> & { kind: keyof typeof FRICTION };

/** A friction cluster (§11G.A1): the total, then each incident (time · #prompt · detail). */
export function frictionClusterTip(marks: FrictionMarkLike[]): TooltipContent {
  const [one] = marks;
  if (!one) return { title: "Friction" };
  if (marks.length === 1) return frictionTip(one);
  const k = FRICTION[one.kind];
  const total = marks.reduce((a, m) => a + Math.max(1, m.count), 0);
  const first = marks.find((m) => m.promptIndex !== null);
  return {
    title: `${k.title} ×${formatExact(total)}`,
    rows: listed(marks, (m) => ({ key: formatDayTime(m.t), value: `${m.promptIndex === null ? "—" : `#${m.promptIndex}`} · ${m.detail}` })),
    hint: first ? `Click to select them and jump to #${first.promptIndex} in Prompts` : "Click to select them",
  };
}

/** A friction glyph. */
export function frictionTip(f: FrictionMarkLike): TooltipContent {
  const k = FRICTION[f.kind];
  const when = `${formatDayTime(f.t)}:${String(new Date(f.t).getSeconds()).padStart(2, "0")}${f.promptIndex === null ? "" : ` · #${f.promptIndex}`}`;
  const rows: TooltipRow[] = [
    { key: "When", value: when },
    { key: k.detail, value: f.detail },
  ];
  if (f.tool) rows.push({ key: "Tool", value: f.tool });
  return { title: f.count > 1 ? `${k.title} ×${f.count}` : k.title, rows, hint: f.promptIndex === null ? undefined : `Click to jump to #${f.promptIndex} in Prompts` };
}

/** The now line. */
export const nowTip = (now: string, following: boolean): TooltipContent => ({
  body: `Now · ${formatTime(now, new Date(now), true)} · ${following ? "following live" : "paused"}`,
});

/** The brush readout while dragging. */
export const brushTip = (from: string, to: string): TooltipContent => ({ body: span(from, to) });

/** Lane definitions (§6B.6 copy). */
export const LANE_DEFINITIONS = {
  modelCalls: "Bars: calls the proxy captured, per bin. Hatched: calls Claude Code reported that the proxy didn’t capture.",
  analytics: "Claude Code analytics events per bin.",
  presence: "Focus per session; sleep per device. Blank = no session open.",
} as const;
