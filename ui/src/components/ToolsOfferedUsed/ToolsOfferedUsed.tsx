import { formatCount } from "../../lib/format";
import type { ToolSourceRow } from "../../lib/tools";
import { fullText } from "../../lib/tips";
import { SkeletonBar } from "../Skeleton/Skeleton";
import { Tooltip } from "../Tooltip/Tooltip";
import "./ToolsOfferedUsed.css";

export type { ToolSourceRow };

export type ToolsOfferedUsedProps = {
  /** One per source: Built-in, then `mcp:<server>` (lib/tools.ts `toolsOffered`, `collapseUnused`). */
  rows: readonly ToolSourceRow[];
  /** `session`: Offered · Used · Calls, the range's; `person`: Most used · Used / offered, all sessions. */
  scope?: "session" | "person";
  /** How often the tool list changed in range (session); null leaves the footer out. */
  changes?: number | null;
  loading?: boolean;
};

const TITLE = "Tools · offered vs used";

/**
 * The tools each request offered against the ones the agent called (handoff §11S, CompTurns
 * ToolsOfferedUsed; Main's ToolsOfferedUsed/person): one row per source, from the tool list
 * every request carries. On the session page it spans two of the three state columns; on the
 * person page it replaces Capabilities.
 */
export function ToolsOfferedUsed({ rows, scope = "session", changes = null, loading }: ToolsOfferedUsedProps) {
  if (loading) {
    return (
      <section aria-label={TITLE} aria-busy className="tou-loading border border-line bg-surface">
        <SkeletonBar width="60%" height={10} />
        <SkeletonBar width="90%" faint />
        <SkeletonBar width="80%" faint />
      </section>
    );
  }
  const person = scope === "person";
  return (
    <section aria-label={TITLE} className={`tou border border-line bg-surface ${person ? "tou-person" : ""}`}>
      <header className={person ? "tou-row justify-between" : "tou-head"}>
        <h3 className="type-key text-ink-faint">{TITLE}</h3>
        <span className="type-data text-ink-muted">{person ? "all sessions · from request tool lists" : "from each request’s tool list"}</span>
        {!person && (
          <>
            <span className="flex-1" />
            <span className="type-data text-ink-faint">in range</span>
          </>
        )}
      </header>
      <div role="table" aria-label={TITLE}>
        <div role="row" className="tou-row type-key text-ink-faint">
          {person ? (
            <>
              <span role="columnheader" className="tou-source">Source</span>
              <span role="columnheader" className="tou-calls">Most used</span>
              <span role="columnheader" className="tou-num">Used / offered</span>
            </>
          ) : (
            <>
              <span role="columnheader" className="tou-source">Source</span>
              <span role="columnheader" className="tou-num">Offered</span>
              <span role="columnheader" className="tou-num">Used</span>
              <span role="columnheader" className="tou-calls">Calls</span>
            </>
          )}
        </div>
        {rows.map((r) =>
          person ? (
            <div role="row" key={r.source} className="tou-row type-data text-ink">
              <span role="cell" className="tou-source type-data-strong">
                {r.source}
              </span>
              <Tooltip kind="truncate" content={r.calls ? fullText(r.calls) : null}>
                <span role="cell" className="tou-calls text-ink-muted">
                  {r.calls ?? "never called"}
                </span>
              </Tooltip>
              <span role="cell" className="tou-num">{`${formatCount(r.used)} / ${formatCount(r.offered)}`}</span>
            </div>
          ) : (
            <div role="row" key={r.source} className="tou-row type-data text-ink">
              <span role="cell" className="tou-source">
                {r.source}
              </span>
              <span role="cell" className="tou-num text-ink-muted">
                {formatCount(r.offered)}
              </span>
              <span role="cell" className="tou-num">
                {formatCount(r.used)}
              </span>
              <Tooltip kind="truncate" content={r.calls ? fullText(r.calls) : null}>
                <span role="cell" className="tou-calls text-ink-muted">
                  {r.calls ?? "—"}
                </span>
              </Tooltip>
            </div>
          ),
        )}
      </div>
      {!person && changes !== null && <p className="tou-foot text-ink-muted">{`tool list changed ${formatCount(changes)} ${changes === 1 ? "time" : "times"} in range`}</p>}
    </section>
  );
}
