// From the person page's payloads (handoff §5.2.3-5.2.7) to what its parts show.
// Pure: the screen and its stories call these.
import type { Schemas } from "../../api/types";
import { reports, type Capabilities, type Capability } from "../../lib/agents";
import { formatCount, formatDuration, formatPct, formatTokens, formatUsd } from "../../lib/format";
import { formatDay, formatTime } from "../../lib/time";
import type { Agent } from "../../components/AgentChip/AgentChip";
import { orgStat } from "../../components/PersonHeader/OrgStat";
import { personSubtitle, type PersonHeaderProps } from "../../components/PersonHeader/PersonHeader";
import { lastSeenValue, missedValue, type PersonStatData } from "../../components/PersonHeader/PersonHeaderRow";
import type { Band } from "../../components/Timeline/BandTrack";
import type { Gap } from "../../components/Timeline/BarTrack";
import type { TimelineMark } from "../../components/Timeline/MarkerTrack";
import type { StateRow } from "../../components/Timeline/RowTrack";
import type { Segment } from "../../components/Timeline/StateTrack";
import type { KeyValueRow } from "../../components/KeyValueList/KeyValueList";
import type { StatTileProps } from "../../components/StatTile/StatTile";

export type PersonHeader = Schemas["PersonHeader"];
export type PersonSummary = Schemas["PersonSummary"];
export type PersonTimeline = Schemas["Timeline"];

/** "4 mo", "1 yr 3 mo": how long since a date. */
function since(iso: string, now: Date): string {
  const months = Math.max(0, Math.round((now.getTime() - Date.parse(iso)) / (30.44 * 86_400_000)));
  return months < 12 ? `${months} mo` : `${Math.floor(months / 12)} yr${months % 12 ? ` ${months % 12} mo` : ""}`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
/** "Aug 2025". */
const monthYear = (iso: string) => {
  const d = new Date(iso);
  return `${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
};
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const stat = (label: string, value: PersonStatData["value"], sub?: string | null, unknownReason?: string): PersonStatData => ({ label, value, sub, unknownReason });

/** "1 · darwin · zsh", or null before any device is seen. */
function devicesValue(p: PersonHeader): string | null {
  if (!p.devices.length) return null;
  const kinds = [...new Set(p.devices.flatMap((d) => [d.platform, d.shell]).filter(Boolean))];
  return [formatCount(p.devices.length), ...kinds].join(" · ");
}

/**
 * The All agents row (handoff §11L.3): the stats every provider fills. Sessions, prompts and
 * tokens follow the range (the summary); devices and last seen are the person's.
 */
export function commonStats(p: PersonHeader, s: PersonSummary | undefined, now: Date): PersonStatData[] {
  const t = s?.ribbon.tokens;
  const tokens = t && t.output !== null ? `${t.input === null ? "—" : formatTokens(t.input, "total")} → ${formatTokens(t.output, "total")}` : null;
  const prompts = s?.footprint.prompts ?? null;
  return [
    stat("Sessions", s ? formatCount(s.ribbon.sessions) : null, null, "the summary hasn’t loaded"),
    stat("Prompts", prompts === null ? null : formatCount(prompts), null, "no prompt ids on these calls"),
    stat("Tokens", tokens, null, "no usage on these calls"),
    stat("Devices", devicesValue(p), null, "no device seen yet"),
    stat("Last seen", p.live ? lastSeenValue("now", true) : p.lastSeenAt ? lastSeenValue(formatTime(p.lastSeenAt, now), false) : null, null, "no traffic yet"),
  ];
}

/** A Claude Code account's Organization stat (§11L.4). */
function orgOf(p: PersonHeader): PersonStatData {
  const o = p.org;
  if (!o) return stat("Organization", null, null, "no organization on the account");
  if (o.kind === "outside") return orgStat({ kind: "outside", id: o.id, personal: p.orgKind === "personal" });
  return orgStat({ kind: o.kind, id: o.id });
}

/** Claude Code's row (§11L.3): Plan, Organization, Role, Using since, 7-day limit, Flags, Bypass, Missed; only the fields it reports. */
function claudeCodeStats(p: PersonHeader, caps: Capabilities, now: Date): PersonStatData[] {
  const has = (f: Capability) => reports(caps, "claude-code", f);
  const l = p.limits;
  const limit = l.utilization7d !== null ? `${Math.round(l.utilization7d)}%` : l.status;
  const f = p.flags;
  const flags = f.total === null ? null : [formatCount(f.total), f.experiments === null ? null : `${formatCount(f.experiments)} exp`].filter(Boolean).join(" · ");
  return [
    has("plan") && stat("Plan", p.plan ? [p.plan, p.rateLimitTier].filter(Boolean).join(" · ") : null, null, "no plan reported yet"),
    has("org") && orgOf(p),
    has("role") && stat("Role", p.role, null, "not extracted yet"),
    has("usingSince") && stat("Using since", p.firstUseAt ? monthYear(p.firstUseAt) : null, p.firstUseAt ? since(p.firstUseAt, now) : null, "first use not reported yet"),
    has("limit7d") && stat("7-day limit", limit, l.overage ? `overage ${l.overage}` : null, "no limit headers yet"),
    has("flags") && stat("Flags", flags, f.changedSince ? `${formatCount(f.changedSince.count)} changed since ${formatTime(f.changedSince.at, now)}` : null, "no flag fetches captured"),
    has("bypass") && stat("Bypass", p.posture.bypassPct === null ? null : formatPct(p.posture.bypassPct), "of model calls", "no permission mode reported"),
    has("missed") && stat("Missed", p.missed === null ? null : missedValue(p.missed), "calls not captured", "no self-report to compare against"),
  ].filter((x): x is PersonStatData => !!x);
}

/** Codex's row (§11L.3): Plan, 5h window, Week (each with its reset), Credits. A window Codex didn't report is left out. */
function codexStats(p: PersonHeader, caps: Capabilities, now: Date): PersonStatData[] {
  const has = (f: Capability) => reports(caps, "codex", f);
  const l = p.agentLimits.find((x) => x.agent === "codex");
  const plan = l?.plan ?? p.accounts.find((a) => a.agent === "codex")?.plan ?? null;
  const window = (name: string, label: string) => {
    const w = l?.windows.find((x) => x.name === name);
    return w ? [stat(label, w.usedPct === null ? null : `${Math.round(w.usedPct)}%`, w.resetsAt ? `resets ${formatTime(w.resetsAt, now)}` : null, "no usage header on the last response")] : [];
  };
  return [
    ...(has("plan") ? [stat("Plan", plan ? `ChatGPT ${cap(plan)}` : null, null, "no plan on the account’s token")] : []),
    ...(has("limits") ? [...window("5h", "5h window"), ...window("week", "Week")] : []),
    ...(has("credits") && l?.credits ? [stat("Credits", l.credits, l.credits === "unlimited" ? null : "balance")] : []),
  ];
}

/** An agent row's account line ("acct 9c2e…41"). */
function accountOf(p: PersonHeader, agent: Agent): string | null {
  const a = p.accounts.find((x) => x.agent === agent)?.accountIdShort ?? (p.accounts.length ? null : p.accountIdShort);
  return a ? `acct ${a}` : null;
}

const AGENT_STATS: Partial<Record<Agent, (p: PersonHeader, c: Capabilities, now: Date) => PersonStatData[]>> = { "claude-code": claudeCodeStats, codex: codexStats };

/**
 * The person header's props (handoff §11L.3): ProviderMark, name and subtitle, the All agents
 * row, then one AgentRow per agent the person uses, each with only that agent's fields (P2).
 */
export function personHeaderProps(p: PersonHeader, s: PersonSummary | undefined, now: Date): PersonHeaderProps {
  const agents = p.agentInfo.map((a) => a.agent);
  return {
    agents,
    linkedBy: p.linkedBy,
    name: p.name ?? "name unknown",
    subtitle: personSubtitle(p.email ?? "email not seen", Math.max(1, p.accounts.length)),
    common: commonStats(p, s, now),
    agentRows: agents.map((agent) => ({ agent, account: accountOf(p, agent), stats: AGENT_STATS[agent]?.(p, p.capabilities, now) ?? [] })),
  };
}

/** The posture chips (Claude Code's, under the header): bypassed approvals, sandbox, token storage, proxy detected (only what's known). */
export function postureChips(p: PersonHeader): string[] {
  const x = p.posture;
  return [
    x.bypassPct !== null && `bypassed approvals ${formatPct(x.bypassPct)} of model calls`,
    x.sandbox && `sandbox ${x.sandbox}`,
    x.tokenStorage && `token storage ${x.tokenStorage}`,
    x.proxyDetected && `proxy detected · ${x.proxyDetected}`,
  ].filter((c): c is string => !!c);
}

/** The ribbon (handoff §7.2): model calls, analytics, account calls, reach, tokens out, cost. */
export function ribbonTiles(s: PersonSummary): StatTileProps[] {
  const r = s.ribbon;
  const sessions = `${formatCount(r.sessions)} ${r.sessions === 1 ? "session" : "sessions"}`;
  return [
    { label: "Model calls", swatch: "model", value: r.modelCalls, sub: s.coverage.notCaptured ? `${sessions} · ${formatCount(s.coverage.notCaptured)} not captured` : sessions },
    { label: "Analytics", swatch: "analytics", value: r.analyticsEvents, sub: `${formatCount(r.analyticsBatches)} batches · ${formatCount(r.eventTypes)} types` },
    { label: "Account calls", swatch: "account", value: r.accountCalls, sub: "no session" },
    { label: "Reach", swatch: "skills", value: r.toolsReachable, unknownReason: "not extracted yet" },
    { label: "Tokens out", swatch: "count", value: r.tokens.output, sub: r.tokens.cacheRead === null ? undefined : `${formatCount(r.tokens.cacheRead)} cache read`, unknownReason: "no usage on these calls" },
    {
      label: "Cost est.",
      value: r.cost.usd === null ? null : formatUsd(r.cost.usd),
      sub: r.cost.estimatedCalls ? `API-equivalent · ${formatCount(r.cost.estimatedCalls)} estimated` : "API-equivalent",
      unknownReason: "no cost reported",
    },
  ];
}

/** A count, or null (unknown: "—" with its reason). */
const count = (n: number | null) => (n === null ? null : formatCount(n));
const list = (xs: string[], n = 6) => (xs.length > n ? `${xs.slice(0, n).join(", ")} +${xs.length - n}` : xs.join(", "));

/** Capabilities · seen in any session. */
export function capabilityRows(s: PersonSummary): KeyValueRow[] {
  const c = s.capabilities;
  const used = (n: number | null) => (n === null ? "" : `${formatCount(n)} used`);
  return [
    { k: "MCP servers", d: list(c.mcpServers.map((m) => m.name)) || "none seen", v: formatCount(c.mcpServers.length) },
    { k: "MCP tools", d: used(c.mcpToolsUsed), v: count(c.mcpToolsTotal), unknownReason: "tool lists not extracted yet" },
    { k: "Built-in tools", d: used(c.builtinUsed), v: count(c.builtinTools), unknownReason: "not extracted yet" },
    { k: "Skills", d: "listed at session start", v: count(c.skills), unknownReason: "not extracted yet" },
    { k: "Hooks", d: c.hookOverheadMs === null ? "" : `~${formatDuration(c.hookOverheadMs)} added per tool call`, v: count(c.hooks), unknownReason: "no hook runs reported" },
    { k: "Memory", d: "long-term writes", v: c.memoryWrites === null ? null : `${formatCount(c.memoryWrites)} ${c.memoryWrites === 1 ? "write" : "writes"}`, unknownReason: "not reported" },
  ];
}

/** Footprint · in range. */
export function footprintRows(s: PersonSummary): KeyValueRow[] {
  const f = s.footprint;
  const tools = Object.entries(f.toolsByName)
    .sort((a, b) => b[1] - a[1])
    .map(([k]) => k);
  return [
    { k: "Prompts", d: `${formatCount(s.ribbon.sessions)} ${s.ribbon.sessions === 1 ? "session" : "sessions"}`, v: f.prompts === null ? null : formatCount(f.prompts), unknownReason: "no prompt ids" },
    { k: "Tool calls", d: tools.slice(0, 6).join(" · "), v: f.toolCalls === null ? null : formatCount(f.toolCalls), unknownReason: "not extracted yet" },
    { k: "Commands", d: f.topCommandWords.slice(0, 6).map((w) => w.word).join(" · "), v: f.commands === null ? null : formatCount(f.commands), unknownReason: "no analytics" },
    {
      k: "Destructive",
      d: `${formatCount(f.destructive.rmF)} rm -f · ${formatCount(f.destructive.rmRf)} rm -rf · ${formatCount(f.destructive.outsideCwd)} outside cwd`,
      v: formatCount(f.destructive.total),
    },
    {
      k: "Code shipped",
      d: [`${formatCount(f.codeShipped.commits)} commits`, `${formatCount(f.codeShipped.pushes)} pushes`, ...f.codeShipped.prs.map((n) => `PR #${n}`)].join(" · "),
      v: `${formatCount(f.codeShipped.prs.length)} ${f.codeShipped.prs.length === 1 ? "PR" : "PRs"}`,
    },
    { k: "Subagent calls", d: "share the parent prompt id", v: f.subagentCalls === null ? null : formatCount(f.subagentCalls), unknownReason: "not extracted yet" },
  ];
}

/** Track props for the person timeline (handoff §6.4). */
export function personLanes(d: PersonTimeline) {
  return {
    window: { from: d.from, to: d.to, now: d.now ?? undefined },
    callBins: d.modelCalls.map((b) => ({ t: b.t, v: b.count })),
    analyticsBins: d.analytics.map((b) => ({ t: b.t, v: b.count })),
    gaps: d.notCaptured.map((g): Gap => ({ from: g.from, to: g.to, count: g.count })),
    bands: d.sessions.map((s): Band => ({ id: s.sessionId, from: s.start, to: s.end, label: s.label, row: s.row, live: s.live, preCapture: s.capturedFromPrompt !== null, capturedFrom: s.capturedFromPrompt })),
    // Rows (§11G.A2): focused, away, device asleep.
    presenceRows: (
      [
        ["focused", "focused", "ink"],
        ["away", "away", "muted"],
        ["asleep", "device asleep", "faint"],
      ] as const
    ).map(([state, name, tone]): StateRow => ({
      name,
      tone,
      segments: d.presence.filter((p) => p.state === state).map((p): Segment => ({ from: p.from, to: p.to, state: p.state, label: p.label })),
    })),
    prompts: d.prompts.map((p): TimelineMark => ({ t: p.t, id: p.promptId, hoverLabel: p.index === null ? undefined : `#${p.index}` })),
    friction: d.friction,
    rows: Math.max(1, ...d.sessions.map((s) => s.row + 1)),
  };
}

/** The nearest activity before and after an empty range, from the timeline's sessions. */
export function nearest(d: PersonTimeline | undefined, from: string, to: string) {
  if (!d) return {};
  const a = Date.parse(from);
  const b = Date.parse(to);
  const before = d.sessions.filter((s) => Date.parse(s.end ?? d.to) < a).sort((x, y) => Date.parse(y.end ?? d.to) - Date.parse(x.end ?? d.to))[0];
  const after = d.sessions.filter((s) => Date.parse(s.start) > b).sort((x, y) => Date.parse(x.start) - Date.parse(y.start))[0];
  return { earlier: before?.end ?? undefined, later: after?.start };
}

/** "Oct 22 – Nov 4". */
export const windowLabel = (from: string, to: string) => `${formatDay(from)} – ${formatDay(to)}`;
