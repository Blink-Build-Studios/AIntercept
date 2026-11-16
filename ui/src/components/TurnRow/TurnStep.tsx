import { fullText } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import "../DataTable/DataTable.css";
import "./TurnRow.css";

export type TurnStepProps = {
  /** `step 22`. */
  label: string;
  /** In --ink-muted: model · tokens · duration · its tool call (`sonnet · 96k → 610 · 4.1 s · Bash npm · exit 1 · 18 s`). */
  detail: string;
  /** Opens today's drawer on this call. */
  onOpen: () => void;
  selected?: boolean;
  pending?: boolean;
  "data-force"?: "hover" | "focus";
};

/**
 * One step, one model call (handoff §11R, CompTurns TurnStep): `step N ·` and what it did; the
 * whole row opens the request drawer on the call (`raw request ›`).
 */
export function TurnStep({ label, detail, onOpen, selected, pending, "data-force": force }: TurnStepProps) {
  return (
    <button
      type="button"
      role="row"
      tabIndex={-1}
      data-roving
      aria-selected={selected || undefined}
      data-pending={pending || undefined}
      data-force={force}
      onClick={onOpen}
      className="dt-row type-data text-ink turn-step"
    >
      <Tooltip kind="truncate" content={fullText(`${label} · ${detail}`)}>
        <span role="cell">
          {`${label} · `}
          <span className="text-ink-muted">{detail}</span>
        </span>
      </Tooltip>
      <span role="cell" className="turn-link">
        raw request ›
      </span>
    </button>
  );
}
