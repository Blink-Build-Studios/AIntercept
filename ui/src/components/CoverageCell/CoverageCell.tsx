import type { Schemas } from "../../api/types";
import { formatCount } from "../../lib/format";
import { coverageTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Marker } from "../Marker/Marker";
import { Tooltip } from "../Tooltip/Tooltip";

type Coverage = Schemas["Coverage"];

/** The `Missed` column (handoff §11G.C2): its header and definition. */
export const MISSED_COLUMN = {
  id: "missed",
  header: "Missed",
  width: 84,
  align: "right" as const,
  definition: "Model calls Claude Code reported that the proxy didn’t capture, in this window.",
};

/**
 * Model calls Claude Code reported that the proxy didn't capture (§11G.C2): `▲ 96`
 * in danger; `0` (muted) when every reported call was captured; `—` with no
 * self-report to compare. The captured/reported fraction is in the tooltip.
 */
export function CoverageCell({
  coverage: c,
  absent,
}: {
  coverage: Coverage | null;
  /** A product with no self-report to compare against (§11L: no placeholder): an empty cell. */
  absent?: boolean;
}) {
  if (absent) return <span role="cell" />;
  if (!c || c.reported === null) {
    return (
      <Tooltip content={unknownTip("Missed", "no analytics to compare")}>
        <span role="cell" className="dt-right text-ink-muted" {...unknownAttrs("no analytics to compare")}>
          —
        </span>
      </Tooltip>
    );
  }
  return (
    <Tooltip content={coverageTip(c)}>
      {c.notCaptured > 0 ? (
        <Marker kind="coverageGap" role="cell" className="dt-right" label={formatCount(c.notCaptured)} />
      ) : (
        <span role="cell" className="dt-right text-ink-muted">
          0
        </span>
      )}
    </Tooltip>
  );
}
