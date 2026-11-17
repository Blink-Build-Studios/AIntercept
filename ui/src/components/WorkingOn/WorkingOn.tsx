import { fullText } from "../../lib/tips";
import { sessionName } from "../RepoRow/WorkSessionRow";
import { Tooltip } from "../Tooltip/Tooltip";

export type WorkingOnData = {
  /** The live session's repo and its running turn, when the person is working now. */
  live: { repo: string | null; turn: number | null; sessionId?: string | null } | null;
  /** Otherwise the repos (or workspaces) of the window's sessions, most recent first. */
  repos: readonly string[];
  /** A chat surface without repos: `(chat)`. */
  chat?: boolean;
};

/**
 * The cell's words: `aintercept · turn #34` live (§11S, content-free: `session 4c1a…77e0 · turn #34`,
 * a repo needs content), else `observer, notes`, `(chat)`, or `—`.
 */
export function workingOnText(w: WorkingOnData): string {
  if (w.live) {
    const where = w.live.repo ?? (w.live.sessionId ? sessionName(w.live.sessionId) : "session");
    return [where, w.live.turn === null ? null : `turn #${w.live.turn}`].filter(Boolean).join(" · ");
  }
  if (w.repos.length) return w.repos.join(", ");
  return w.chat ? "(chat)" : "—";
}

/**
 * What a person is working on, in the People table (handoff §11R, People.dc.html `WorkingOn`):
 * the live turn and its repo, otherwise the repos; --ink-muted, one line, the rest in the tooltip.
 */
export function WorkingOn({ data }: { data: WorkingOnData }) {
  const text = workingOnText(data);
  return (
    <Tooltip kind="truncate" content={text === "—" ? null : fullText(text)}>
      <span role="cell" className="working-on text-ink-muted">
        {text}
      </span>
    </Tooltip>
  );
}
