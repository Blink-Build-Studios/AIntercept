import { formatCount, formatDuration } from "../../lib/format";
import { useRowLink } from "../../lib/linkedHover";
import { formatTime } from "../../lib/time";
import { fullText, timeTip, turnFrictionTip } from "../../lib/tips";
import { workText, type WorkPart } from "../../lib/work";
import { Marker } from "../Marker/Marker";
import { Tooltip } from "../Tooltip/Tooltip";
import { WorkText } from "../TurnRow/WorkText";
import "../DataTable/DataTable.css";
import "./RepoRow.css";

export type WorkSession = {
  sessionId: string;
  start: string;
  end: string | null;
  live: boolean;
  /** Content-free: `session 4c1a…77e0`; with content: its branch or title. */
  label: string;
  turns: number | null;
  activeMs: number | null;
  /** Interrupted turns and failed calls (§11S). */
  friction: { interrupted: number; failed: number };
};

/** A session named by its id alone (§11S, content-free): `session 4c1a…77e0`. */
export const sessionName = (id: string) => `session ${id.length > 10 ? `${id.slice(0, 4)}…${id.slice(-4)}` : id}`;

/**
 * One session on the Work tab (handoff §11R, §11S, CompTurns `SessionRow/work*`): started ·
 * live dot · label · turns · active · work line · friction (`✕2 !5`) · ›. Content-free the list
 * is flat (`flat`, 12px in); with content it sits under its repo, 38px in. The whole row opens
 * the session.
 */
export function WorkSessionRow({
  session: s,
  work,
  href,
  flat,
  pending,
  now,
  "data-force": force,
}: {
  session: WorkSession;
  work: readonly WorkPart[];
  href: string;
  /** The content-free flat list: no repo above it. */
  flat?: boolean;
  pending?: boolean;
  now?: string;
  "data-force"?: "hover" | "focus" | "linked";
}) {
  const end = s.end ?? now ?? new Date().toISOString();
  const { linked, handlers } = useRowLink({ source: "table", ids: [s.sessionId], span: [s.start, end] }, { id: s.sessionId });
  const fr = s.friction;
  const marks = [fr.interrupted > 0 && `✕${formatCount(fr.interrupted)}`, fr.failed > 0 && `!${formatCount(fr.failed)}`].filter(Boolean).join(" ");
  const words = [fr.interrupted > 0 && `${formatCount(fr.interrupted)} interrupted`, fr.failed > 0 && `${formatCount(fr.failed)} failed calls`].filter(Boolean);
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
      className={`dt-row type-data text-ink work-session-row ${flat ? "work-session-row-flat" : ""}`}
    >
      <Tooltip content={timeTip(s.start)}>
        <span role="cell" className="text-ink-muted">
          {formatTime(s.start, now ? new Date(now) : undefined)}
        </span>
      </Tooltip>
      {s.live ? (
        <Marker kind="live" compact role="cell" />
      ) : (
        <span role="cell" aria-hidden />
      )}
      <Tooltip kind="truncate" content={fullText(s.label)}>
        <span role="cell">{s.label}</span>
      </Tooltip>
      <span role="cell" className="dt-right">
        {s.turns === null ? "—" : formatCount(s.turns)}
      </span>
      <span role="cell" className="dt-right text-ink-muted">
        {s.activeMs === null ? "—" : formatDuration(s.activeMs)}
      </span>
      <Tooltip kind="truncate" content={line ? fullText(line) : null}>
        <span role="cell" className="repo-row-work text-ink-muted">
          <WorkText parts={work} />
        </span>
      </Tooltip>
      <Tooltip content={turnFrictionTip(fr.interrupted, fr.failed)}>
        <span role="cell" aria-label={words.join(", ") || undefined}>
          {marks}
        </span>
      </Tooltip>
      <span role="cell" aria-hidden className="text-ink-faint">
        ›
      </span>
    </a>
  );
}
