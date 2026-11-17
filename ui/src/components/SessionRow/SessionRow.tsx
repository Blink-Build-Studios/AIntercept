import type { KeyboardEvent, MouseEvent, ReactNode } from "react";
import type { Schemas } from "../../api/types";
import { formatCount, formatDuration } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { useRowLink } from "../../lib/linkedHover";
import { fullText, timeTip, turnFrictionTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import type { Column } from "../DataTable/DataTable";
import { Marker } from "../Marker/Marker";
import { Tooltip } from "../Tooltip/Tooltip";
import { AgentChip } from "../AgentChip/AgentChip";
import "./SessionRow.css";

export type Session = Schemas["SessionRow"];

// Canonical columns (handoff §11A.6); the table's min width is SESSION_MIN_WIDTH.
export const SESSION_MIN_WIDTH = 1200;
export const SESSION_COLUMNS: Column[] = [
  { id: "start", header: "Start", width: 92 },
  { id: "live", header: "", width: 10 },
  { id: "repo", header: "Repo · branch", width: 240 },
  { id: "label", header: "Label", width: "flex" },
  { id: "prompts", header: "Prompts", width: 64, align: "right" },
  { id: "span", header: "Span", width: 64, align: "right", definition: "First to last activity in the session." },
  { id: "calls", header: "Calls", width: 56, align: "right" },
  { id: "friction", header: "Friction", width: 84, definition: "✕ interrupted turns, counted per session." },
  { id: "go", header: "", width: 12 },
];

// A person with more than one agent, in All agents (handoff §11M 2.7, CompProviders
// `SessionTable/mixed`): only the columns every agent fills (P1), with an Agent column after
// the live dot and the canonical 240px Repo. Scoped to Codex the Agent column goes
// (SESSION_COMMON_COLUMNS); scoped to Claude Code the table is the canonical one. With nested
// threads (`nesting`) the table is `expandable` (a treegrid).
const COMMON_TAIL: Column[] = [
  { id: "label", header: "Label", width: "flex" },
  { id: "prompts", header: "Prompts", width: 64, align: "right" },
  { id: "span", header: "Span", width: 64, align: "right", definition: "First to last activity in the session." },
  { id: "calls", header: "Calls", width: 56, align: "right" },
  { id: "friction", header: "Friction", width: 84, definition: "✕ interrupted turns, counted per session." },
  { id: "go", header: "", width: 12 },
];
export const SESSION_AGENT_MIN_WIDTH = 1200;
export const SESSION_AGENT_COLUMNS: Column[] = [
  { id: "start", header: "Started", width: 92 },
  { id: "live", header: "", width: 10 },
  { id: "agent", header: "Agent", width: 140 },
  { id: "repo", header: "Repo", width: 240 },
  ...COMMON_TAIL,
];
export const SESSION_COMMON_MIN_WIDTH = 1100;
export const SESSION_COMMON_COLUMNS: Column[] = [
  { id: "start", header: "Started", width: 92 },
  { id: "live", header: "", width: 10 },
  { id: "repo", header: "Repo", width: 240 },
  ...COMMON_TAIL,
];

/** An untitled Codex thread's label (§11M 3.4): `untitled · thread 01a1…aed1`. */
export const untitledThread = (id: string) => `untitled · thread ${id.slice(0, 4)}…${id.slice(-4)}`;

/** A thread's place among its relatives (§11J gap 3): a parent with a twisty, or a child indented under it. */
export type SessionNesting = { kind: "parent"; expanded: boolean; onToggle: () => void } | { kind: "child"; agentName: string };

/** A right-aligned count cell; unknown is the cell itself showing "—" with its reason. */
const Num = ({ v, label, reason, muted, mark }: { v: number | null; label: string; reason: string; muted?: boolean; mark?: ReactNode }) => (
  <Tooltip content={v === null ? unknownTip(label, reason) : null}>
    <span role="cell" className={`dt-right ${muted ? "text-ink-muted" : ""}`} {...(v === null ? unknownAttrs(reason) : {})}>
      {v === null ? "—" : formatCount(v)}
      {mark}
    </span>
  </Tooltip>
);

/**
 * One session, linking to its page (handoff §6.5, columns §11A.6). Live sessions
 * show ● in `live` and no end; ended ones a hairline ●, so the column never collapses.
 */
export function SessionRow({
  session: s,
  href,
  pending,
  now,
  agentColumn,
  nesting,
  threadFacts,
  "data-force": force,
}: {
  session: Session;
  href: string;
  pending?: boolean;
  now?: string;
  /** Draws the Agent column (SESSION_AGENT_COLUMNS): the agent chip, with its client after a muted dot ("exec", "tui"). Implies `common`. */
  agentColumn?: { client?: string | null };
  nesting?: SessionNesting;
  /** Thread facts after the label, in --ink-faint: "· 3 subagent · 1 review", "· fork of 3b9a… at #4". */
  threadFacts?: string | null;
  /** Pins a state for stories; "linked": the timeline points at this session (§6B.5). */
  "data-force"?: "hover" | "focus" | "linked";
}) {
  const end = s.end ?? now ?? new Date().toISOString();
  // An untitled thread (§11M 3.4) reads `untitled · thread 01a1…aed1` in --ink-faint.
  const untitled = s.label.source === "none" && s.agent === "codex";
  const label = untitled ? untitledThread(s.sessionId) : s.label.text;
  // Hover and focus put the session's band and span on the timeline; its band hovered marks the row.
  const { linked, handlers } = useRowLink({ source: "table", ids: [s.sessionId], span: [s.start, end] }, { id: s.sessionId });
  // Friction (§11S): interrupted turns only; the analytics kinds are gone.
  const interrupted = s.friction.interrupted;
  // Claude Code: repo · branch; Codex: workspace folder · short commit (no branch).
  const repo = s.repo ? `${s.repo}${s.branch ? ` · ${s.branch}` : s.commit ? ` · ${s.commit}` : ""}` : null;
  const parent = nesting?.kind === "parent" ? nesting : null;
  const child = nesting?.kind === "child" ? nesting : null;
  // The twisty sits in the row's link: a click on it toggles the children instead of opening the session; → and ← do the same from the keyboard.
  const onClick = parent
    ? (e: MouseEvent) => {
        if (!(e.target as Element).closest(".session-twisty")) return;
        e.preventDefault();
        parent.onToggle();
      }
    : undefined;
  const onKeyDown = parent
    ? (e: KeyboardEvent) => {
        // Treegrid keys: → opens a closed parent, ← closes an open one; otherwise the table moves focus.
        if (e.target === e.currentTarget && ((e.key === "ArrowRight" && !parent.expanded) || (e.key === "ArrowLeft" && parent.expanded))) {
          e.preventDefault();
          e.stopPropagation();
          parent.onToggle();
        }
      }
    : undefined;
  return (
    <a
      href={href}
      role="row"
      tabIndex={-1}
      data-roving
      data-pending={pending || undefined}
      data-linked={linked || undefined}
      data-force={force}
      aria-expanded={parent ? parent.expanded : undefined}
      onClick={onClick}
      onKeyDown={onKeyDown}
      {...handlers}
      className={`dt-row dt-tall type-data text-ink ${child ? "session-row-child" : ""}`}
    >
      <Tooltip content={timeTip(s.start)}>
        <span role="cell" className="text-ink-muted">
          {formatTime(s.start, now ? new Date(now) : undefined)}
        </span>
      </Tooltip>
      {s.live ? (
        <Marker kind="live" compact role="cell" />
      ) : (
        <span role="cell" aria-hidden className="text-line">
          ●
        </span>
      )}
      {agentColumn && (
        <span role="cell">
          <AgentChip agent={s.agent} client={agentColumn.client ?? undefined} />
        </span>
      )}
      {child ? (
        // A subagent or review thread under its parent: `⤷ <agent_name>` in place of the repo, indented inside the cell (§11M 2.2).
        <Tooltip kind="truncate" content={fullText(child.agentName)}>
          <span role="cell" className="type-data-strong">
            <span className="session-child-name text-ink-faint">{"⤷ "}</span>
            {child.agentName}
          </span>
        </Tooltip>
      ) : (
        <Tooltip kind={repo ? "truncate" : "cell"} content={repo ? fullText(repo) : unknownTip("Repo", s.fieldsMissing ? "identity and repo not parsed" : "not extracted yet")}>
          <span role="cell" className="type-data-strong" {...(repo ? {} : unknownAttrs(s.fieldsMissing ? "identity and repo not parsed" : "not extracted yet"))}>
            {parent && (
              <>
                <span aria-hidden className="session-twisty text-ink-muted">
                  {parent.expanded ? "▾ " : "▸ "}
                </span>
              </>
            )}
            {repo ?? "—"}
          </span>
        </Tooltip>
      )}
      <Tooltip kind="truncate" content={fullText(threadFacts ? `${label} ${threadFacts}` : label)}>
      <span role="cell" className="text-ink-muted">
        {untitled ? <span className="text-ink-faint">{label}</span> : label}
        {threadFacts && (
          <>
            {" "}
            <span className="text-ink-faint">{threadFacts}</span>
          </>
        )}
      </span>
      </Tooltip>
      <Num v={s.prompts} label="Prompts" reason="no prompt ids on these calls" />
      <span role="cell" className="dt-right text-ink-muted">
        {formatDuration(Date.parse(end) - Date.parse(s.start))}
      </span>
      <Num v={s.modelCalls} label="Calls" reason="" />
      <Tooltip content={turnFrictionTip(interrupted, 0)}>
        <span role="cell" aria-label={interrupted > 0 ? `${formatCount(interrupted)} interrupted` : undefined}>
          {interrupted > 0 ? `✕${formatCount(interrupted)}` : ""}
        </span>
      </Tooltip>
      <span role="cell" aria-hidden className="text-ink-faint">
        ›
      </span>
    </a>
  );
}
