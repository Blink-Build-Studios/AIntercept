import { workText, type WorkPart } from "../../lib/work";
import { fullText } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import { WorkText } from "./WorkText";
import "../DataTable/DataTable.css";
import "./TurnRow.css";

export type SubagentRowProps = {
  /** Its name, from the spawn call's input (`test-writer`): needs recorded content. */
  name: string | null;
  /** The order it was spawned in: content-free, it reads `subagent N`. */
  n: number;
  /** Its own steps and work: `9 steps · Write ×1 · Bash ×2`, or with content `9 steps · wrote 1 file (.test.ts) +66`. */
  work: readonly WorkPart[];
  /** Its tokens, in → out. */
  tokens: string | null;
  /** Its thread: Codex's own session page. */
  href?: string;
  /** Or an action (Claude Code's sidechain: the drawer on its first call). */
  onOpen?: () => void;
};

/**
 * A subagent under the turn that spawned it (handoff §11R, §11S, CompTurns SubagentRow):
 * `⤷ subagent N` content-free, `⤷ subagent <name>` with content; its work and tokens, and
 * `open thread ›`. On --bg, so it reads as a level down.
 */
export function SubagentRow({ name, n, work, tokens, href, onOpen }: SubagentRowProps) {
  const line = workText(work);
  return (
    <div role="row" className="dt-row type-data text-ink subagent-row">
      <span role="cell" aria-hidden className="text-ink-muted">
        ⤷
      </span>
      {name === null ? (
        <span role="cell">{`subagent ${n}`}</span>
      ) : (
        <span role="cell">
          {"subagent "}
          <b>{name}</b>
        </span>
      )}
      <Tooltip kind="truncate" content={line ? fullText(line) : null}>
        <span role="cell" className="text-ink-muted">
          <WorkText parts={work} />
        </span>
      </Tooltip>
      <span role="cell" className="dt-right">
        {tokens ?? "—"}
      </span>
      <span role="cell">
        {href ? (
          <a href={href} className="turn-link">
            open thread ›
          </a>
        ) : onOpen ? (
          <button type="button" className="turn-link" onClick={onOpen}>
            open thread ›
          </button>
        ) : null}
      </span>
    </div>
  );
}
