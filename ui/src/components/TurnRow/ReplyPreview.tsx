import "../DataTable/DataTable.css";
import "./TurnRow.css";

export type ReplyPreviewProps = {
  /** The reply's first two lines, recorded content only. */
  text: string;
  /** Its size: `1.2k tokens`. */
  size: string;
  /** Shows it all (a ContentBlock, rendered). */
  onExpand?: () => void;
};

/**
 * The final reply under its group when content is recorded (handoff §11R, CompTurns ReplyPreview):
 * two lines in a --ch-model-ruled block, then `expand · <size>`.
 */
export function ReplyPreview({ text, size, onExpand }: ReplyPreviewProps) {
  return (
    <div role="row" className="dt-row type-data text-ink reply-preview">
      <div role="cell" className="reply-preview-text">
        {`${text} `}
        {/* Inline in the text, so a span with the button role (a button can't run inline). */}
        <span
          role="button"
          tabIndex={0}
          className="turn-link"
          onClick={onExpand}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              onExpand?.();
            }
          }}
        >
          {`expand · ${size}`}
        </span>
      </div>
    </div>
  );
}
