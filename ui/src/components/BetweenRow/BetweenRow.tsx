import type { Schemas } from "../../api/types";
import { formatDuration } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { timeTip } from "../../lib/tips";
import { unknownTip } from "../../lib/unknown";
import { Tooltip } from "../Tooltip/Tooltip";
import "./BetweenRow.css";

export type Between = Schemas["BetweenData"];

// Compactions read in the count channel; recaps and calls outside a prompt in ink-muted (handoff §6.5).
const TONE: Record<Between["kind"], string> = {
  compaction: "text-ch-count",
  "idle-compaction": "text-ch-count",
  recap: "text-ink-muted",
  call: "text-ink-muted",
};

/** The row's text: the server's detail (it names compactions and recaps itself), a `call` prefix for calls outside any prompt, and the wait. */
export function betweenText(b: Between): string {
  const parts = [b.kind === "call" ? `call · ${b.detail}` : b.detail];
  if (b.waitMs !== null) parts.push(`${formatDuration(b.waitMs)} wait`);
  return parts.join(" · ");
}

export type BetweenRowProps = {
  between: Between;
  /** Opens the request drawer on the call behind the row; rows with a requestId become buttons with `open ›`. */
  onOpen?: (requestId: string) => void;
  selected?: boolean;
  pending?: boolean;
  /** The reference "now" for same-day times (defaults to the clock). */
  now?: string;
};

/**
 * A row between prompts in the Prompts tab (handoff §6.5, §5.2.9): compaction,
 * idle compaction, recap, or a model call outside any prompt. It sits in the
 * prompt table's first columns (twisty 18 · # 40 · time 92), then its text at
 * content width (§11B.4), as a quiet row (time and `—` faint, the detail keeps its tone); a row backed by a captured call opens
 * the drawer like a StepRow, with `open ›` after the text.
 */
export function BetweenRow({ between: b, onOpen, selected, pending, now }: BetweenRowProps) {
  const requestId = onOpen ? b.requestId : null;
  const openable = requestId !== null;
  const cells = (
    <>
      <span role="cell" />
      <Tooltip content={unknownTip("Prompt", "outside any prompt")}>
        <span role="cell" className="text-ink-faint" aria-label="no prompt">
          —
        </span>
      </Tooltip>
      <Tooltip content={timeTip(b.t)}>
        <span role="cell" className="text-ink-muted">
          {formatTime(b.t, now ? new Date(now) : undefined)}
        </span>
      </Tooltip>
      <span role="cell" className={`quiet-keep ${TONE[b.kind]}`}>
        {betweenText(b)}
      </span>
      {openable && (
        <span role="cell" className="text-ink-muted">
          open ›
        </span>
      )}
    </>
  );
  const className = "dt-row dt-quiet type-data between-row text-ink";
  if (requestId === null || !onOpen) {
    return (
      <div role="row" className={className}>
        {cells}
      </div>
    );
  }
  return (
    <button
      type="button"
      role="row"
      tabIndex={-1}
      data-roving
      aria-selected={selected || undefined}
      data-pending={pending || undefined}
      onClick={() => onOpen(requestId)}
      className={className}
    >
      {cells}
    </button>
  );
}
