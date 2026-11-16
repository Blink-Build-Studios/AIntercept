import type { ReactNode } from "react";
import type { Schemas } from "../../api/types";
import { fullText } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import "../DataTable/DataTable.css";
import "./TurnRow.css";

/** What a run of consecutive steps did (handoff §11R): a new group starts when the kind changes. */
export type WorkGroupKind = Schemas["WorkGroup"]["kind"];

export type WorkGroupProps = {
  kind: WorkGroupKind;
  /** Verb, count and target: `Read 5 files`, `Ran npm test ×4`, `Thinking`, `Reply`. */
  title: string;
  /** In --ink-muted after it: types or paths, steps, time (`.ts ×5 · 4 steps · 41 s`). */
  meta: string;
  /** A command group's exit codes, on the right (`exit 1 · exit 1 · exit 0`). */
  exits?: readonly (number | null)[];
  expanded: boolean;
  onToggle: () => void;
  /** The group's TurnSteps (and a reply's preview), shown when expanded. */
  children?: ReactNode;
  "data-force"?: "hover" | "focus";
};

/** Up to 4 exit codes in order (`exit 1 · exit 1 · exit 0`); more, tallied (`exit 0 ×13 · exit 1 ×9`). */
export function exitLine(exits: readonly (number | null)[]): string {
  const word = (e: number | null) => (e === null ? "exit —" : `exit ${e}`);
  if (exits.length <= 4) return exits.map(word).join(" · ");
  const n = new Map<string, number>();
  for (const e of exits) n.set(word(e), (n.get(word(e)) ?? 0) + 1);
  return [...n.entries()].sort((a, b) => b[1] - a[1]).map(([w, c]) => `${w} ×${c}`).join(" · ");
}

/**
 * One group inside an open turn (handoff §11R, CompTurns WorkGroup): consecutive steps by what
 * they did. The row toggles its steps; a reply's twisty is `↩`. Command groups list their exit
 * codes on the right, in --ink-muted (a failure is work, never red).
 */
export function WorkGroup({ kind, title, meta, exits, expanded, onToggle, children, "data-force": force }: WorkGroupProps) {
  const codes = exitLine(exits ?? []);
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
        className="dt-row type-data text-ink work-group"
      >
        <span role="cell" aria-hidden className="text-ink-muted">
          {kind === "reply" ? "↩" : expanded ? "▾" : "▸"}
        </span>
        <Tooltip kind="truncate" content={fullText(`${title} ${meta}`)}>
          <span role="cell">
            {`${title} `}
            <span className="text-ink-muted">{meta}</span>
          </span>
        </Tooltip>
        <span role="cell" className="text-ink-muted">
          {codes}
        </span>
      </button>
      {expanded && children}
    </>
  );
}
