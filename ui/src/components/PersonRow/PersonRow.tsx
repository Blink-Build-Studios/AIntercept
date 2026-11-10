import type { Schemas } from "../../api/types";
import { formatCount, formatPct, formatTokens } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { timeTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { AGENT_NAME, AgentChip, type Agent } from "../AgentChip/AgentChip";
import type { Column } from "../DataTable/DataTable";
import { CoverageCell, MISSED_COLUMN } from "../CoverageCell/CoverageCell";
import { SkeletonBar } from "../Skeleton/Skeleton";
import { Sparkline } from "../Sparkline/Sparkline";
import { Tooltip } from "../Tooltip/Tooltip";
import "./PersonRow.css";

export type Person = Schemas["PersonSummaryRow"];

/**
 * The People table's scope (handoff §11L.1): All agents (the default) has only the columns
 * every provider fills; an agent view appends that agent's own columns before the chevron.
 */
export type PeopleView = "all" | Agent;

// Canonical columns (handoff §11A.6, §11L.1; People.dc.html). The table's min width is PERSON_MIN_WIDTH.
export const PERSON_MIN_WIDTH = 1240;
const COMMON: Column[] = [
  { id: "person", header: "Person", width: 250 },
  { id: "agents", header: "Agents", width: 260 },
  { id: "seen", header: "Last seen", width: 100 },
  { id: "sessions", header: "Sessions", width: 70, align: "right" },
  { id: "prompts", header: "Prompts", width: 70, align: "right" },
  { id: "tokens", header: "Tokens", width: 90, align: "right", definition: "Output tokens. ≈ when some calls had no usage." },
  { id: "activity", header: "Activity · 14 days", width: "flex" },
];
const GO: Column = { id: "go", header: "", width: 12 };
/** Each agent view's own columns (§11L.1): Claude Code's Bypass and Missed, Codex's limits and plan. */
const AGENT_COLUMNS: Partial<Record<Agent, Column[]>> = {
  "claude-code": [{ id: "bypass", header: "Bypass", width: 90, align: "right", definition: "Share of calls made with permission mode bypassPermissions." }, MISSED_COLUMN],
  codex: [
    { id: "limits", header: "5h · week", width: 150, align: "right", definition: "Codex usage: the share of the 5-hour and weekly windows used, at the last response." },
    { id: "plan", header: "Plan", width: 100 },
  ],
};
/** The columns of a view: the common ones, the agent's own, the chevron. */
export const personColumns = (view: PeopleView = "all"): Column[] => [...COMMON, ...(view === "all" ? [] : (AGENT_COLUMNS[view] ?? [])), GO];
/** The Claude Code view's columns: CompRows draws its PersonRow states there. */
export const PERSON_COLUMNS: Column[] = personColumns("claude-code");

/** Agents with a permission mode and their own report to compare against (coding agents); chat surfaces read "—". */
const REPORTS = new Set<Person["agents"][number]>(["claude-code", "codex"]);

const plural = (n: number, one: string) => `${formatCount(n)} ${n === 1 ? one : `${one}s`}`;
/** "sam@…": the local part only, so the line stays short. */
const emailShort = (email: string) => `${email.split("@")[0] ?? email}@…`;

/**
 * The id line under a name (handoff §11L.1): `email · N accounts · N devices`, or
 * `linked by email · …` for a linked person (in an agent view, `linked by email · Codex
 * account only`). A web surface has no account: `ChatGPT web · 1 device`. Org and account
 * ids live on the person page, not here.
 */
export function personIdLine(p: Person, view: PeopleView = "all"): string {
  const devices = plural(p.devices, "device");
  if (p.linkedBy && p.accounts > 1) {
    return view === "all" ? `linked by ${p.linkedBy} · ${plural(p.accounts, "account")} · ${devices}` : `linked by ${p.linkedBy} · ${AGENT_NAME[view]} account only`;
  }
  if (!p.accountIdShort) return `${AGENT_NAME[p.agents[0] ?? "claude-code"]} web · ${devices}`;
  return `${p.email ? emailShort(p.email) : "email not seen"} · ${plural(Math.max(1, p.accounts), "account")} · ${devices}`;
}

/** "● now", "4 min ago" within the hour, else the time. */
function lastSeen(p: Person, now: Date) {
  if (p.live) return { text: "● now", live: true };
  const ago = now.getTime() - Date.parse(p.lastSeenAt);
  if (ago >= 0 && ago < 3_600_000) return { text: `${Math.max(1, Math.round(ago / 60_000))} min ago`, live: false };
  return { text: formatTime(p.lastSeenAt, now), live: false };
}

/**
 * Codex's limits cell: the 5-hour and weekly windows used (`12% · 31%`). A window at 80%
 * or more is set apart in its own span, so the one near its limit can be found.
 */
function LimitsCell({ limits }: { limits: (number | null)[] | null }) {
  if (!limits?.length) {
    return (
      <Tooltip content={unknownTip("5h · week", "no rate-limit headers seen")}>
        <span role="cell" className="dt-right text-ink-muted" {...unknownAttrs("no rate-limit headers seen")}>
          —
        </span>
      </Tooltip>
    );
  }
  const high = Math.max(...limits.map((l) => l ?? 0));
  const text = (l: number | null) => (l === null ? "—" : `${Math.round(l)}%`);
  return (
    <span role="cell" className="dt-right">
      {limits.map((l, i) => {
        const sep = i < limits.length - 1 ? " · " : "";
        return l !== null && l === high && l >= 80 ? (
          <span key={i} className="text-ink">
            {text(l)}
            {sep}
          </span>
        ) : (
          `${text(l)}${sep}`
        );
      })}
    </span>
  );
}

/**
 * One person, linking to their page (handoff §6.5, columns §11A.6, views §11L.1): name and
 * id line, agents, last seen, counts, output tokens (≈ when estimated), 14 days of
 * activity on the table's shared scale; then the view's own cells: Claude Code's bypass
 * and missed calls, or Codex's limits and plan.
 */
export function PersonRow({
  person: p,
  href,
  activityMax,
  now,
  pending,
  view = "all",
  idLine,
  limits,
  plan,
  "data-force": force,
}: {
  person: Person;
  href: string;
  /** The table's busiest day, so every row's sparkline shares one scale. */
  activityMax?: number;
  now?: string;
  pending?: boolean;
  /** The table's scope: All agents (common cells only) or one agent's view, with its own cells (§11L.1). */
  view?: PeopleView;
  /** The id line, when the caller has its own; else `personIdLine(person, view)`. */
  idLine?: string;
  /** Codex view: the 5-hour and weekly windows' used percentages, in that order. */
  limits?: (number | null)[] | null;
  /** Codex view: the account's plan ("Plus"). */
  plan?: string | null;
  "data-force"?: "hover" | "focus";
}) {
  const at = now ? new Date(now) : new Date();
  const seen = lastSeen(p, at);
  const reports = p.agents.some((a) => REPORTS.has(a));
  const ids = idLine ?? personIdLine(p, view);
  const info = new Map(p.agentInfo.map((i) => [i.agent, i]));
  const tokens =
    p.tokensOut === null || p.tokensAccuracy === "missing"
      ? { text: "—", tone: "text-ink-faint" }
      : { text: `${p.tokensAccuracy === "estimated" ? "≈ " : ""}${formatTokens(p.tokensOut, "total")} out`, tone: p.tokensAccuracy === "estimated" ? "text-ink-muted" : "text-ink" };
  const cov = p.coverage;
  return (
    <a href={href} role="row" tabIndex={-1} data-roving data-pending={pending || undefined} data-force={force} className="dt-row person-row type-data text-ink">
      <span role="cell" className="person-cell">
        <span className="person-name">{p.name ?? "name unknown"}</span>
        <span className="person-ids text-ink-faint">{ids}</span>
      </span>
      <span role="cell" className="person-agents">
        {p.agents.map((a) => {
          const i = info.get(a);
          return <AgentChip key={a} agent={a} info={i ? { version: i.version, firstSeenAt: i.firstSeenAt, devices: i.devices } : undefined} />;
        })}
      </span>
      <Tooltip content={timeTip(p.lastSeenAt)}>
        <span role="cell" className={seen.live ? "text-live" : "text-ink-muted"}>
          {seen.text}
        </span>
      </Tooltip>
      <span role="cell" className="dt-right">
        {formatCount(p.sessions)}
      </span>
      <Tooltip content={p.prompts === null ? unknownTip("Prompts", "no prompt ids on these calls") : null}>
        <span role="cell" className="dt-right" {...(p.prompts === null ? unknownAttrs("no prompt ids on these calls") : {})}>
          {p.prompts === null ? "—" : formatCount(p.prompts)}
        </span>
      </Tooltip>
      <span role="cell" className={`dt-right ${tokens.tone}`}>
        {tokens.text}
      </span>
      <span role="cell" className="person-activity">
        <Sparkline
          values={p.activity.map((d) => d.count)}
          max={activityMax}
          color={view !== "all" && p.agents.includes(view) ? view : (p.agents[0] ?? "claude-code")}
          aria-label={`${formatCount(p.activity.reduce((n, d) => n + d.count, 0))} model calls in 14 days`}
        />
      </span>
      {view === "claude-code" &&
        (!reports ? (
          // An agent with no permission mode or self-report reads "—" (§11G.C2), its reason on the cell.
          <Tooltip content={unknownTip("Bypass", "this agent reports no permission mode")}>
            <span role="cell" className="dt-right" {...unknownAttrs("this agent reports no permission mode")}>
              —
            </span>
          </Tooltip>
        ) : (
          <Tooltip content={p.bypassPct === null ? unknownTip("Bypass", "no permission mode reported") : null}>
            <span role="cell" className="dt-right" {...(p.bypassPct === null ? unknownAttrs("no permission mode reported") : {})}>
              {p.bypassPct === null ? "—" : formatPct(p.bypassPct)}
            </span>
          </Tooltip>
        ))}
      {view === "claude-code" && <CoverageCell coverage={reports ? cov : null} />}
      {view === "codex" && <LimitsCell limits={limits ?? null} />}
      {view === "codex" && (
        <span role="cell" className={plan ? undefined : "text-ink-muted"} {...(plan ? {} : unknownAttrs("no plan on the account's token"))}>
          {plan ?? "—"}
        </span>
      )}
      <span role="cell" aria-hidden className="text-ink-faint">
        ›
      </span>
    </a>
  );
}

/** A loading row at the loaded row's size (handoff §6A.3; CompRows "PersonRow/loading"; People.dc.html: common cells only). */
export function PersonRowSkeleton({ view = "all" }: { view?: PeopleView }) {
  const right = (w: number) => (
    <span role="cell" className="flex justify-end">
      <SkeletonBar width={w} />
    </span>
  );
  const extra = view === "all" ? 0 : (AGENT_COLUMNS[view]?.length ?? 0);
  return (
    <div role="row" aria-busy className="dt-row person-row person-row-loading">
      <span role="cell" className="person-loading-name">
        <SkeletonBar width="60%" />
        <SkeletonBar width="70%" height={8} faint />
      </span>
      <span role="cell">
        <SkeletonBar width={96} height={16} />
      </span>
      <span role="cell">
        <SkeletonBar width={70} />
      </span>
      {right(30)}
      {right(30)}
      {right(54)}
      <span role="cell" className="person-activity">
        <span className="person-activity-skeleton bg-skeleton" />
      </span>
      {view === "claude-code" && right(44)}
      {view === "claude-code" && right(54)}
      {view !== "claude-code" && Array.from({ length: extra }, (_, i) => <span key={i} role="cell" />)}
      <span role="cell" />
    </div>
  );
}
