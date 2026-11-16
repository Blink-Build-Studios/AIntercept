import type { Schemas } from "../../api/types";
import { formatCount, formatDuration } from "../../lib/format";
import { useRowLink } from "../../lib/linkedHover";
import { formatTime } from "../../lib/time";
import { frictionTip, fullText, timeTip } from "../../lib/tips";
import { workText, type WorkPart } from "../../lib/work";
import { Marker } from "../Marker/Marker";
import { PartialMark } from "../PartialMark/PartialMark";
import { Tooltip } from "../Tooltip/Tooltip";
import { WorkText } from "../TurnRow/WorkText";
import "../DataTable/DataTable.css";
import "./RepoRow.css";

export type WorkSession = {
  sessionId: string;
  start: string;
  end: string | null;
  live: boolean;
  label: string;
  turns: number | null;
  /** Capture began at this prompt (§11G.D1): the `†` after the turn count. */
  capturedFromPrompt: number | null;
  activeMs: number | null;
  friction: Schemas["Friction"];
};

/**
 * One session under its repo on the Work tab (handoff §11R, CompTurns `SessionRow/workDefault`):
 * started · live dot · label · turns (with † for a partial capture) · active · work line ·
 * friction · ›. Indented 38px; the whole row opens the session.
 */
export function WorkSessionRow({ session: s, work, href, pending, now, "data-force": force }: { session: WorkSession; work: readonly WorkPart[]; href: string; pending?: boolean; now?: string; "data-force"?: "hover" | "focus" | "linked" }) {
  const end = s.end ?? now ?? new Date().toISOString();
  const { linked, handlers } = useRowLink({ source: "table", ids: [s.sessionId], span: [s.start, end] }, { id: s.sessionId });
  const fr = s.friction;
  const calm = [fr.frustrated > 0 && `↯${fr.frustrated}`, fr.interrupted > 0 && `✕${fr.interrupted}`].filter(Boolean).join(" ");
  const words = [fr.frustrated > 0 && `${fr.frustrated} frustrated`, fr.interrupted > 0 && `${fr.interrupted} interrupted`, fr.destructive > 0 && `${fr.destructive} destructive`].filter(Boolean);
  const line = workText(work);
  return (
    <a
      href={href}
      role="row"
      tabIndex={-1}
      data-roving
      data-pending={pending || undefined}
      data-linked={linked || undefined}
      data-force={force}
      {...handlers}
      className="dt-row type-data text-ink work-session-row"
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
      <Tooltip kind="truncate" content={fullText(s.label)}>
        <span role="cell">{s.label}</span>
      </Tooltip>
      <span role="cell" className="dt-right">
        {s.turns === null ? "—" : formatCount(s.turns)}
        {s.capturedFromPrompt !== null && <PartialMark from={s.capturedFromPrompt} />}
      </span>
      <span role="cell" className="dt-right text-ink-muted">
        {s.activeMs === null ? "—" : formatDuration(s.activeMs)}
      </span>
      <Tooltip kind="truncate" content={line ? fullText(line) : null}>
        <span role="cell" className="repo-row-work text-ink-muted">
          <WorkText parts={work} />
        </span>
      </Tooltip>
      <Tooltip content={frictionTip(fr)}>
        <span role="cell" aria-label={words.join(", ") || undefined}>
          {calm}
          {calm && fr.destructive > 0 && " "}
          {fr.destructive > 0 && <Marker kind="destructive" count={fr.destructive} />}
        </span>
      </Tooltip>
      <span role="cell" aria-hidden className="text-ink-faint">
        ›
      </span>
    </a>
  );
}
