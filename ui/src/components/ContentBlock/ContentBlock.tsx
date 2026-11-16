import { useState } from "react";
import { formatCount } from "../../lib/format";
import "./ContentBlock.css";

/** Over either limit a block starts collapsed to its preview (handoff §11R, CompTurns ContentBlock). */
export const COLLAPSE_LINES = 12;
export const COLLAPSE_BYTES = 4096;
/** The preview's length, and the most lines an expanded block shows before `open in raw ›`. */
export const PREVIEW_LINES = 4;
export const EXPAND_LINES = 400;

/** Whether content of this size starts collapsed. */
export const collapses = (lines: number, bytes: number) => lines > COLLAPSE_LINES || bytes > COLLAPSE_BYTES;

/** The lines a collapsed block shows: the head for files, prompts and replies, the tail for command output. */
export function previewLines(lines: readonly string[], from: "head" | "tail", n = PREVIEW_LINES): string[] {
  return from === "head" ? lines.slice(0, n) : lines.slice(Math.max(0, lines.length - n));
}

export type ContentBlockProps = {
  /** What the content is, in the key style: `Pasted into prompt`, `Tool result · Bash npm test`, `Reply`. */
  label: string;
  /** Its size before expanding: `18.4 KB · 412 lines · .csv`, `3.4k tokens · 212 lines`, `exit 1 · 18 s`. */
  meta: string;
  /**
   * The recorded content, at most EXPAND_LINES lines of it (never load more than is shown);
   * `total` is the whole content's line count. Null without recorded content: the header and
   * `note` only, no preview and no expand.
   */
  content?: { lines: readonly string[]; total: number; bytes: number; from: "head" | "tail" } | null;
  /** The footer line; defaults to `head 4 of 412 lines · <raw> ›`. Without content: what metadata gave. */
  note?: string;
  /** The raw link's words: `full text in raw request`, `raw response`. */
  rawLabel?: string;
  /** Opens the drawer on the call (`open in raw ›`). */
  onOpenRaw?: () => void;
  /** Starts expanded (stories). */
  defaultExpanded?: boolean;
};

/**
 * Long content inside a turn (handoff §11R, CompTurns ContentBlock): a header with what it is and
 * its size; over 12 lines or 4 KB it shows a 4-line preview (the head, or the tail for command
 * output) with `expand`, which shows up to 400 lines and then `open in raw ›`. Without recorded
 * content: the header and what metadata gives.
 */
export function ContentBlock({ label, meta, content = null, note, rawLabel = "raw request", onOpenRaw, defaultExpanded = false }: ContentBlockProps) {
  const long = content ? collapses(content.total, content.bytes) : false;
  const [open, setOpen] = useState(defaultExpanded);
  const shown = !content ? [] : !long || open ? content.lines.slice(0, EXPAND_LINES) : previewLines(content.lines, content.from);
  const more = content && open && content.total > shown.length;
  const footer =
    note ??
    (content && long && !open ? `${content.from} ${PREVIEW_LINES} of ${formatCount(content.total)} lines · ${rawLabel} ›` : more ? `${formatCount(shown.length)} of ${formatCount(content.total)} lines` : null);
  return (
    <section className="content-block border border-line bg-surface" aria-label={label}>
      <div className="content-block-head">
        <span className="type-key text-ink-faint">{label}</span>
        <span className="type-data text-ink-muted">{meta}</span>
        <span className="flex-1" />
        {content && long && (
          <button type="button" className="content-block-link type-data text-ink" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            {open ? "collapse" : "expand"}
          </button>
        )}
      </div>
      {content && (
        <div className="content-block-body">
          <div className={`content-block-text ${long && !open ? "content-block-clip" : ""}`}>{shown.join("\n")}</div>
        </div>
      )}
      {(footer || more) && (
        <div className="content-block-foot text-ink-faint">
          {footer}
          {more && onOpenRaw && (
            <>
              {" · "}
              <button type="button" className="content-block-link" onClick={onOpenRaw}>
                open in raw ›
              </button>
            </>
          )}
        </div>
      )}
    </section>
  );
}
