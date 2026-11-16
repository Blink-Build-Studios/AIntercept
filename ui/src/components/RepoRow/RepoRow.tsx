import type { ReactNode } from "react";
import { formatCount, formatDuration } from "../../lib/format";
import { fullText } from "../../lib/tips";
import { workText, type WorkPart } from "../../lib/work";
import type { Column } from "../DataTable/DataTable";
import { Tooltip } from "../Tooltip/Tooltip";
import { WorkText } from "../TurnRow/WorkText";
import "../DataTable/DataTable.css";
import "./RepoRow.css";

// The Work tab (handoff §11R, CompTurns RepoRow · SessionRow (work)): ▸ 14 · Started 92 · ● 10 ·
// Repo · session 260 · Turns 60 · Active 70 · Work (flex) · Friction 84 · › 12; min width 1100.
export const WORK_MIN_WIDTH = 1100;
export const WORK_COLUMNS: Column[] = [
  { id: "twisty", header: "", width: 14 },
  { id: "start", header: "Started", width: 92 },
  { id: "live", header: "", width: 10 },
  { id: "repo", header: "Repo · session", width: 260 },
  { id: "turns", header: "Turns", width: 60, align: "right" },
  { id: "active", header: "Active", width: 70, align: "right", definition: "Time inside turns: prompt to last step, summed." },
  { id: "work", header: "Work", width: "flex", definition: "What the sessions did, from their steps' tool calls and the agent's analytics." },
  { id: "friction", header: "Friction", width: 84, definition: "↯ frustrated · ✕ interrupted · ▲ destructive commands, counted per session." },
  { id: "go", header: "", width: 12 },
];

export type RepoRowProps = {
  /** The repo (Claude Code) or workspace (Codex); null: `repo not parsed`. */
  repo: string | null;
  /** After the name, in --ink-muted: the one session's label, or when (`before Nov 2`). */
  sub?: string | null;
  /** A mixed person: the agent chip(s) after the repo name. */
  agents?: ReactNode;
  sessions: number;
  turns: number | null;
  activeMs: number | null;
  work: readonly WorkPart[];
  expanded: boolean;
  onToggle: () => void;
  /** Its sessions (WorkSessionRows), shown when expanded. */
  children?: ReactNode;
  "data-force"?: "hover" | "focus";
};

/**
 * One repo on the person page's Work tab (handoff §11R, CompTurns RepoRow): its session count,
 * turns, active time and work line. Expanded it is --surface-raised, its sessions below.
 */
export function RepoRow({ repo, sub, agents, sessions, turns, activeMs, work, expanded, onToggle, children, "data-force": force }: RepoRowProps) {
  const line = workText(work);
  return (
    <>
      <button
        type="button"
        role="row"
        tabIndex={-1}
        data-roving
        aria-expanded={expanded}
        data-force={force}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if ((e.key === "ArrowRight" && !expanded) || (e.key === "ArrowLeft" && expanded)) {
            e.preventDefault();
            e.stopPropagation();
            onToggle();
          }
        }}
        className="dt-row type-data text-ink repo-row"
      >
        <span role="cell" aria-hidden className="text-ink-muted">
          {expanded ? "▾" : "▸"}
        </span>
        <span role="cell" className="text-ink-faint">
          {`${formatCount(sessions)} ${sessions === 1 ? "session" : "sessions"}`}
        </span>
        <Tooltip kind="truncate" content={fullText([repo ?? "repo not parsed", sub].filter(Boolean).join(" · "))}>
          <span role="cell" className="type-data-strong">
            {repo ?? <span className="text-ink-muted">repo not parsed</span>} {agents}
            {sub && <span className="repo-row-sub text-ink-muted">{`· ${sub}`}</span>}
          </span>
        </Tooltip>
        <span role="cell" className="dt-right">
          {turns === null ? "—" : formatCount(turns)}
        </span>
        <span role="cell" className="dt-right text-ink-muted">
          {activeMs === null ? "—" : formatDuration(activeMs)}
        </span>
        <Tooltip kind="truncate" content={line ? fullText(line) : null}>
          <span role="cell" className="repo-row-work text-ink-muted">
            <WorkText parts={work} />
          </span>
        </Tooltip>
        <span role="cell" />
        <span role="cell" />
      </button>
      {expanded && children}
    </>
  );
}
