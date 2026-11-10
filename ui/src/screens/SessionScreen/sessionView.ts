// From the session page's payloads to what its parts show (handoff §11L.5-7). Pure: the
// screen and its tests call these.
import type { Schemas } from "../../api/types";
import { AGENT_NAME } from "../../components/AgentChip/AgentChip";
import type { Channel } from "../../components/ChannelSwatch/ChannelSwatch";
import { CODEX_CHANNELS, type RequestChannel } from "../../components/RequestFilters/RequestFilters";
import { modelCallDetail, type Request, type RequestDetail } from "../../components/RequestRow/RequestRow";
import type { SessionTab as TabSpec } from "../../components/SessionTabs/SessionTabs";
import { tabHints } from "../../components/SessionTabs/TabHint";
import { formatCount } from "../../lib/format";
import { formatDayTime } from "../../lib/time";
import { untitledThread } from "../../components/SessionRow/SessionRow";

const idShort = (id: string) => (id.length > 12 ? `${id.slice(0, 4)}…${id.slice(-4)}` : id);

/**
 * The session's title and the line beside it (§11L.5, §11M 3.4). Claude Code: repo · branch.
 * A Codex thread: its generated title (label source `title`) or first prompt; an untitled
 * thread is `<workspace> · <first-turn start>` (`pomodoro · Nov 9 09:00`), its line
 * `untitled · thread 01a1…aed1 · <sha>`. The breadcrumb uses the same title.
 */
export function sessionTitle(s: Header): { title: string; sub: string } {
  const has = (f: Header["capabilities"][number]) => s.capabilities.includes(f);
  if (s.agent === "codex" && s.label.source === "none") {
    return {
      title: [s.repo ?? "untitled", formatDayTime(s.start)].join(" · "),
      sub: [untitledThread(s.sessionId), s.commit].filter(Boolean).join(" · "),
    };
  }
  const repoTitle = has("repoBranch") && s.repo ? `${s.repo}${s.branch ? ` · ${s.branch}` : s.commit ? ` · ${s.commit}` : ""}` : null;
  const workspace = has("workspace") ? [s.repo, s.commit].filter(Boolean).join(" · ") : "";
  return { title: repoTitle ?? s.label.text, sub: [workspace || null, `session ${idShort(s.sessionId)}`].filter(Boolean).join(" · ") };
}

type Header = Schemas["SessionHeader"];
type Counts = Schemas["RangeCounts"];

const plural = (n: number, one: string, many = `${one}s`) => `${formatCount(n)} ${n === 1 ? one : many}`;

/**
 * The tabs' labels with count and unit (§11L.7): `Prompts 1 · 203 steps`, `Requests 203 calls ·
 * 412 other`, `Events 58`. The shared model-call count links Prompts and Requests.
 */
export function sessionTabs(c: Counts | undefined): TabSpec[] {
  const other = c ? Math.max(0, c.requests - c.modelCalls) : 0;
  return [
    { id: "prompts", label: "Prompts", count: c ? `${formatCount(c.prompts)} · ${plural(c.modelCalls, "step")}` : null },
    { id: "requests", label: "Requests", count: c ? `${plural(c.modelCalls, "call")}${other ? ` · ${formatCount(other)} other` : ""}` : null },
    { id: "events", label: "Events", count: c ? formatCount(c.events) : null },
  ];
}

/** Channels no prompt starts, in the Requests hint's words. */
const UNPROMPTED: Partial<Record<Channel, string>> = { usage: "limits", catalog: "catalog", metrics: "metrics", account: "account", flags: "flags", count: "token counts" };

/** The active tab's TabHint (§11L.7). Codex adds that it calls a prompt a turn. */
export function tabHint(tab: string, s: Header | undefined, c: Counts | undefined, kindCounts: Record<string, number> | undefined, person: string | null): string {
  if (tab === "requests") {
    const channels = s?.channels ?? [];
    return tabHints.requests(channels.filter((ch) => (kindCounts?.[ch] ?? 1) > 0).flatMap((ch) => (UNPROMPTED[ch] ? [UNPROMPTED[ch]] : [])));
  }
  if (tab === "events") return `The analytics events ${s ? AGENT_NAME[s.agent] : "the agent"} sent during this session, in time order.`;
  const detail = c ? `${plural(c.prompts, "prompt")}, ${plural(c.modelCalls, "model call")} as steps` : "";
  return tabHints.prompts(person ?? "this person", detail, s?.agent === "codex" ? "Codex calls a prompt a turn." : null);
}

/** Each channel's chip label; Codex's from CODEX_CHANNELS. */
const LABEL: Record<Channel, string> = {
  model: "model",
  analytics: "analytics",
  flags: "flags",
  count: "count",
  account: "account",
  skills: "skills",
  other: "other",
  ...Object.fromEntries(CODEX_CHANNELS.map((c) => [c.channel, c.label])),
} as Record<Channel, string>;

/** The Requests chips (§11L.6): the agent's declared channels with their counts (RequestFilters drops the zeros). */
export function requestChannels(s: Header | undefined, kindCounts: Record<string, number> | undefined): RequestChannel[] {
  const channels: Channel[] = s?.channels ?? (Object.keys(kindCounts ?? {}) as Channel[]);
  return channels.map((channel) => ({ channel, label: LABEL[channel] ?? channel, count: kindCounts?.[channel] ?? 0 }));
}

/** Tokens in for the detail cell: Codex's input already counts its cache reads; Claude Code's adds them. */
function tokensIn(r: Request): number | null {
  const t = r.tokens;
  if (!t || t.accuracy === "missing") return null;
  if (r.agent === "codex") return t.input;
  const parts = [t.input, t.cacheRead, t.cacheWrite];
  return parts.every((x) => x === null) ? null : parts.reduce<number>((a, x) => a + (x ?? 0), 0);
}

/** What a model call produced (§11L.6, §11M 3.1): its tool calls (`exec ×2`), the first words of its
 * text when bodies are recorded, or `text` for a text-only call without them. */
function produced(r: Request): string | null {
  if (r.toolCalls.length) return r.toolCalls.join(", ");
  if (r.text) return `“${r.text}…”`;
  return r.tokens && r.tokens.output !== null && r.tokens.output > 0 ? "text" : null;
}

/**
 * A request row's detail (§11L.6). A model call: `step <n>` (with `#<prompt>` first where the
 * table has no `#` column, §11M 1.5), model and effort, tokens, what it produced, stop reason.
 * A hidden call names its kind (`no output` for a prewarm). Another channel: the endpoint or
 * event the API names; with none, the row keeps its path.
 */
export function requestDetail(r: Request, { withPrompt = false }: { withPrompt?: boolean } = {}): RequestDetail | undefined {
  if (!r.captured) return undefined;
  if (r.kind === "model") {
    if (r.hiddenKind) return { head: r.hiddenKind, facts: [r.model, r.tokens?.output === 0 ? "no output" : null].filter((x): x is string => !!x) };
    const out = r.tokens && r.tokens.accuracy !== "missing" ? r.tokens.output : null;
    return modelCallDetail({
      prompt: withPrompt ? r.promptIndex : null,
      step: r.stepIndex,
      model: r.model,
      effort: r.effort,
      tokensIn: tokensIn(r),
      tokensOut: out,
      produced: produced(r),
      stop: r.stopReason,
    });
  }
  return r.detail ? { head: r.detail } : undefined;
}

/** The channels a person's All agents requests share (§11M 3.3): model, analytics, account & usage, other. */
export const SHARED_CHANNELS: { channel: Channel; label: string; members: Channel[] }[] = [
  { channel: "model", label: "model", members: ["model"] },
  { channel: "analytics", label: "analytics", members: ["analytics"] },
  { channel: "account", label: "account & usage", members: ["account", "usage"] },
  { channel: "other", label: "other", members: ["flags", "count", "skills", "metrics", "catalog"] },
];

/** A row's shared channel in All agents (§11M 3.3). */
export const sharedChannel = (kind: Channel): Channel => SHARED_CHANNELS.find((c) => c.members.includes(kind))?.channel ?? "other";

/** The All agents chips: the shared channels with the summed counts of their members. */
export function sharedRequestChannels(kindCounts: Record<string, number> | undefined): RequestChannel[] {
  return SHARED_CHANNELS.map(({ channel, label, members }) => ({ channel, label, count: members.reduce((n, m) => n + (kindCounts?.[m] ?? 0), 0) }));
}
