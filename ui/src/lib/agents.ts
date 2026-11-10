import type { Schemas } from "../api/types";
import type { ThreadOriginData } from "../components/ThreadOrigin/ThreadOrigin";
import { formatDuration } from "./format";

type Agent = Schemas["AgentInfo"]["agent"];
/** A provider-specific field an agent reports (handoff §11L field audit). */
export type Capability = Schemas["SessionHeader"]["capabilities"][number];
/** A request channel an agent declares (§11L.6). */
export type Channel = Schemas["SessionHeader"]["channels"][number];
/** `capabilities[agent]` as the overview and person header serve it. */
export type Capabilities = Partial<Record<string, readonly Capability[]>>;

/**
 * Whether `agent` reports `field` (handoff §11L, P2): the UI renders a provider-specific
 * field only when it's listed, with no placeholder when it isn't.
 */
export function reports(capabilities: Capabilities | undefined, agent: Agent, field: Capability): boolean {
  return !!capabilities?.[agent]?.includes(field);
}

/** Whether any of `agents` reports `field` (a mixed table keeps the column; the other rows leave the cell empty). */
export const anyReports = (capabilities: Capabilities | undefined, agents: readonly Agent[], field: Capability) =>
  agents.some((a) => reports(capabilities, a, field));

type Session = Schemas["SessionRow"];

/** A thread id as rows show it ("3b9a…"). */
export const threadShort = (id: string) => `${id.slice(0, 4)}…`;

/** The facts after a Codex thread's label (handoff §11J gap 3, 4): "· 3 subagent · 1 review",
 * "· fork of 3b9a… at #4", "· resumed from 3b9a…"; null when there are none. */
export function threadFacts(s: Session): string | null {
  const parts: string[] = [];
  if (s.children?.subagent) parts.push(`${s.children.subagent} subagent`);
  if (s.children?.review) parts.push(`${s.children.review} review`);
  const o = s.origin;
  if (o?.kind === "fork" && o.sessionId) parts.push(`fork of ${threadShort(o.sessionId)}${o.turn ? ` at #${o.turn}` : ""}`);
  if (o?.kind === "resume") parts.push(o.sessionId ? `resumed from ${threadShort(o.sessionId)}` : "resumed");
  return parts.length ? parts.map((x) => `· ${x}`).join(" ") : null;
}

/** Whether a row is a subagent or review thread nested under a parent that's also listed. */
export const isNested = (s: Session, ids: Set<string>) =>
  (s.origin?.kind === "subagent" || s.origin?.kind === "review") && !!s.origin.sessionId && ids.has(s.origin.sessionId);

const TOOL_KIND: Record<string, string> = { shell: "shell", mcp: "MCP", fileChange: "file change", webSearch: "web search", subagent: "subagent", dynamic: "dynamic" };
const secs = (ms: number) => (ms < 1000 ? `${Math.round(ms)} ms` : formatDuration(ms));

/** Codex's turn report as TurnReport's parts (handoff §11J gap 6), absent fields left out:
 * "sampling 2.4 s · 7 requests · 1 retry · tools: shell 4, file change 2 · steers 0 · trigger user". */
export function turnReportParts(r: Schemas["TurnReport"]): string[] {
  const tools = Object.entries(r.toolCalls).map(([k, n]) => `${TOOL_KIND[k] ?? k} ${n}`);
  return [
    r.samplingMs !== null && `sampling ${secs(r.samplingMs)}`,
    r.requests !== null && `${r.requests} ${r.requests === 1 ? "request" : "requests"}`,
    r.retries !== null && `${r.retries} ${r.retries === 1 ? "retry" : "retries"}`,
    tools.length > 0 && `tools: ${tools.join(", ")}`,
    r.steers !== null && `steers ${r.steers}`,
    r.compactionMs !== null && `compaction ${secs(r.compactionMs)}`,
    r.trigger !== null && `trigger ${r.trigger}`,
  ].filter((x): x is string => typeof x === "string");
}

type SessionHeader = Schemas["SessionHeader"];
const sessionHref = (id: string) => `/sessions/${encodeURIComponent(id)}`;

/** The session header's facts line (handoff §11J gap 1): cwd, shell, tz, approval, sandbox, as reported. */
export function sessionFacts(h: SessionHeader): { k: string; v: string }[] {
  const f = h.facts;
  if (!f) return [];
  return (["cwd", "shell", "tz", "approval", "sandbox"] as const).flatMap((k) => {
    const v = f[k];
    return v ? [{ k, v }] : [];
  });
}

/** Where a thread came from, for ThreadOrigin (handoff §11J gaps 3, 4); null for a fresh thread. */
export function threadOrigin(h: Schemas["SessionRow"]): ThreadOriginData | null {
  const o = h.origin;
  if (!o) return null;
  const src = o.sessionId ?? h.sessionId;
  const base = { threadShort: threadShort(src), href: sessionHref(src) };
  if (o.kind === "fork") return { kind: "fork", ...base, turn: o.turn ?? 0, init: o.init === "warm_fork" ? "warm_fork" : "cold_fork" };
  if (o.kind === "resume") return { kind: "resume", ...base, init: "cold_resume" };
  return { kind: o.kind, ...base, turn: o.turn, init: o.kind === "review" ? "review" : "thread_spawn" };
}
