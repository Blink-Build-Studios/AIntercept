import { workText, type WorkPart } from "../../lib/work";
import { fullText } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import { WorkText } from "./WorkText";
import "../DataTable/DataTable.css";
import "./TurnRow.css";

export type SubagentRowProps = {
  /** The subagent's name or type (`test-writer`). */
  name: string;
  /** Its own steps and work: `9 steps · wrote 1 file (.test.ts) +66 · ran npm test ×2: passed`. */
  work: readonly WorkPart[];
  /** Its tokens, in → out. */
  tokens: string | null;
  /** Its thread: Codex's own session page. */
  href?: string;
  /** Or an action (Claude Code's sidechain: the drawer on its first call). */
  onOpen?: () => void;
};

/**
 * A subagent under the turn that spawned it (handoff §11R, CompTurns SubagentRow): `⤷ subagent
 * <name>`, its work and tokens, and `open thread ›`. On --bg, so it reads as a level down.
 */
export function SubagentRow({ name, work, tokens, href, onOpen }: SubagentRowProps) {
  const line = workText(work);
  return (
    <div role="row" className="dt-row type-data text-ink subagent-row">
      <span role="cell" aria-hidden className="text-ink-muted">
        ⤷
      </span>
      <span role="cell">
        {"subagent "}
        <b>{name}</b>
      </span>
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
