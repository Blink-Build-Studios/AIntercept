import type { Schemas } from "../../api/types";
import { formatCount } from "../../lib/format";
import { formatRange } from "../../lib/time";
import { coverageLineTip } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import "./CoverageLine.css";

export type CoverageLineProps = {
  coverage: Schemas["Coverage"];
  onView?: () => void;
  client?: string;
  now?: Date;
  /** Pins hover on the gap line, for stories. */
  "data-force"?: "hover";
};

/** "A, B and C". */
const list = (xs: string[]) => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

/** Quiet when complete; a danger line naming each window when not (handoff §6.3, §5.4; CompSummary "CoverageLine"). */
export function CoverageLine({ coverage, onView, client = "Claude Code", now, "data-force": force }: CoverageLineProps) {
  const frame = "coverage-line flex flex-wrap items-center gap-3 border border-line";
  if (coverage.notCaptured === 0) {
    return (
      <div className={frame}>
        <span className="type-data text-ink-muted">✓ coverage complete</span>
        {coverage.reported !== null && (
          <span className="type-body text-ink-muted">
            All {formatCount(coverage.reported)} model calls {client} reported were captured.
          </span>
        )}
      </div>
    );
  }
  const n = formatCount(coverage.notCaptured);
  const windows = list(coverage.windows.map((w) => `${formatRange(w.from, w.to, now)} (${formatCount(w.count)})`));
  return (
    // The gap explains itself (§6B.7): the whole line hovers; the tooltip names the windows, the longest, and why.
    <Tooltip content={coverageLineTip(coverage, now, !!onView)}>
      <div role="status" className={`${frame} coverage-line-tip`} data-force={force}>
        <span className="type-data-strong text-danger">▲ coverage gap</span>
        <span className="type-body text-ink">
          {`${client} reported ${n} model calls the proxy never saw${windows ? `: ${windows}` : ""}.`}
        </span>
        {onView && (
          <button type="button" onClick={onView} className="link type-data text-start">
            view {n} calls
          </button>
        )}
      </div>
    </Tooltip>
  );
}
