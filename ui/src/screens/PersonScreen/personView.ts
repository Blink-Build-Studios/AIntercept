// From the person page's payloads (handoff §5.2.3-5.2.7) to what its parts show.
// Pure: the screen and its stories call these.
import type { Schemas } from "../../api/types";
import { reports, type Capabilities, type Capability } from "../../lib/agents";
import { formatCount, formatDuration, formatTokens } from "../../lib/format";
import { formatDay, formatTime } from "../../lib/time";
import type { Agent } from "../../components/AgentChip/AgentChip";
import { orgStat } from "../../components/PersonHeader/OrgStat";
import { personSubtitle, type PersonHeaderProps } from "../../components/PersonHeader/PersonHeader";
import { lastSeenValue, type PersonStatData } from "../../components/PersonHeader/PersonHeaderRow";
import type { Band } from "../../components/Timeline/BandTrack";
import type { TimelineMark } from "../../components/Timeline/MarkerTrack";
import type { KeyValueRow } from "../../components/KeyValueList/KeyValueList";
import type { OutcomeFact } from "../../components/OutcomeSlot/OutcomeFacts";
import { outcomeFacts } from "../../lib/turns";

export type PersonHeader = Schemas["PersonHeader"];
export type PersonSummary = Schemas["PersonSummary"];
export type PersonTimeline = Schemas["Timeline"];

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const stat = (label: string, value: PersonStatData["value"], sub?: string | null, unknownReason?: string): PersonStatData => ({ label, value, sub, unknownReason });

/**
 * The All agents row (handoff §11L.3, §11S): the stats every provider fills. Sessions, turns and
 * tokens follow the range (the summary); last seen is the person's. No devices (analytics).
 */
export function commonStats(p: PersonHeader, s: PersonSummary | undefined, now: Date): PersonStatData[] {
  const t = s?.ribbon.tokens;
  const tokens = t && t.output !== null ? `${t.input === null ? "—" : formatTokens(t.input, "total")} → ${formatTokens(t.output, "total")}` : null;
  const w = s?.work;
  const edited = w ? (w.outcome.editedFiles ?? w.work.editedFiles ?? (w.work.edits || null)) : null;
  const lines = w && w.work.linesAdded !== null && w.work.linesRemoved !== null ? `+${formatCount(w.work.linesAdded)} −${formatCount(w.work.linesRemoved)}` : null;
  return [
    stat("Sessions", s ? formatCount(s.ribbon.sessions) : null, null, "the summary hasn’t loaded"),
    stat("Turns", w ? formatCount(w.turns) : null, null, "the summary hasn’t loaded"),
    stat("Tokens", tokens, null, "no usage on these calls"),
    stat("Active", w ? formatDuration(w.activeMs) : null, w ? "turn time" : null, "the summary hasn’t loaded"),
    stat("Edited", edited === null ? null : `${formatCount(edited)} ${edited === 1 ? "file" : "files"}`, lines, "no file edits reported"),
    stat("Commits", w ? formatCount(w.outcome.commits) : null, w ? `${formatCount(w.repos)} ${w.repos === 1 ? "repo" : "repos"}` : null, "the summary hasn’t loaded"),
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

/**
 * Claude Code's row (§11L.3, §11S): Plan, Organization, Role, 7-day limit, all from account and
 * auth calls and rate-limit headers; only the fields it reports.
 */
function claudeCodeStats(p: PersonHeader, caps: Capabilities): PersonStatData[] {
  const has = (f: Capability) => reports(caps, "claude-code", f);
  const l = p.limits;
  const limit = l.utilization7d !== null ? `${Math.round(l.utilization7d)}%` : l.status;
  return [
    has("plan") && stat("Plan", p.plan ? [p.plan, p.rateLimitTier].filter(Boolean).join(" · ") : null, null, "no plan reported yet"),
    has("org") && orgOf(p),
    has("role") && stat("Role", p.role, null, "not extracted yet"),
    has("limit7d") && stat("7-day limit", limit, l.overage ? `overage ${l.overage}` : null, "no limit headers yet"),
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
    agentRows: agents.map((agent) => ({
      agent,
      account: accountOf(p, agent),
      stats: AGENT_STATS[agent]?.(p, p.capabilities, now) ?? [],
    })),
  };
}

/** The OutcomeSlot's title for the window: `This fortnight`, `This week`, `Today`, `All time`. */
export const outcomeTitle = (window: string) => ({ "24h": "Today", "7d": "This week", "14d": "This fortnight" })[window] ?? "All time";

/** The facts the person's OutcomeSlot shows (§11R): what the range shipped, as captured; zeros left out. */
export const personFacts = (s: PersonSummary): OutcomeFact[] => outcomeFacts(s.work.outcome, {});

/** Footprint · in range (§11S: Commands, Destructive and Code shipped are gone). */
export function footprintRows(s: PersonSummary): KeyValueRow[] {
  const f = s.footprint;
  const tools = Object.entries(f.toolsByName)
    .sort((a, b) => b[1] - a[1])
    .map(([k]) => k);
  return [
    { k: "Turns", d: `${formatCount(s.ribbon.sessions)} ${s.ribbon.sessions === 1 ? "session" : "sessions"}`, v: f.turns === null ? null : formatCount(f.turns), unknownReason: "no prompt ids" },
    { k: "Tool calls", d: tools.slice(0, 6).join(" · "), v: f.toolCalls === null ? null : formatCount(f.toolCalls), unknownReason: "not extracted yet" },
    { k: "Subagent calls", d: "share the parent prompt id", v: f.subagentCalls === null ? null : formatCount(f.subagentCalls), unknownReason: "not extracted yet" },
  ];
}

/** Track props for the person timeline (handoff §6.4). */
export function personLanes(d: PersonTimeline) {
  return {
    window: { from: d.from, to: d.to, now: d.now ?? undefined },
    // Work per bin (§11R): edits on top of everything else.
    workBins: d.work.map((b) => ({ t: b.t, edits: b.edits, other: b.other })),
    bands: d.sessions.map((s): Band => ({ id: s.sessionId, from: s.start, to: s.end, label: s.label, row: s.row, live: s.live, preCapture: s.capturedFromPrompt !== null })),
    turns: d.prompts.map((p): TimelineMark => ({ t: p.t, id: p.promptId, hoverLabel: p.index === null ? undefined : `#${p.index}` })),
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
