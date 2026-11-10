import { forwardRef, type HTMLAttributes } from "react";

/** `limitReached`: a usage window at 100%, the account blocked (handoff §11J gap 5): a coverage-like failure. */
export type MarkerKind = "frustrated" | "interrupted" | "destructive" | "coverageGap" | "limitReached" | "match" | "live";

const GLYPH: Record<MarkerKind, string> = {
  frustrated: "↯",
  interrupted: "✕",
  destructive: "▲",
  coverageGap: "▲",
  limitReached: "▲",
  match: "✓",
  live: "●",
};
const WORD: Record<MarkerKind, string> = {
  frustrated: "frustrated",
  interrupted: "interrupted",
  destructive: "destructive",
  coverageGap: "reported, not captured",
  limitReached: "usage limit reached",
  match: "matches the client's report",
  live: "live",
};

export type MarkerProps = {
  kind: MarkerKind;
  count?: number;
  label?: string;
  /** Glyph only (a bare ● in a cell or beside a value), in the surrounding font; the words move to the accessible name. */
  compact?: boolean;
  /** Glyph and words in the surrounding font (a stat value: `▲ 94` at the value's size and weight). */
  inherit?: boolean;
  /** Render the marker as the table cell itself, so it fills the cell as on the canvas. */
  role?: "cell";
  className?: string;
} & Omit<HTMLAttributes<HTMLSpanElement>, "role" | "className">;

/**
 * A status glyph, always with words (handoff §3.2, §6.2). With `count` it renders
 * compact (`↯1`, `▲10`), and the words move to the accessible name.
 * `danger` only for destructive commands, coverage gaps and a reached usage limit; `live` only for the live dot.
 */
export const Marker = forwardRef<HTMLSpanElement, MarkerProps>(function Marker({ kind, count, label, compact = false, inherit = false, role, className = "", ...rest }, ref) {
  const tone = kind === "destructive" || kind === "coverageGap" || kind === "limitReached" ? "quiet-keep text-danger" : kind === "live" ? "text-live" : "text-ink";
  const words = label ?? WORD[kind];
  if (count !== undefined || compact) {
    const name = count !== undefined ? `${count} ${words}` : words;
    return (
      <span ref={ref} role={role ?? "img"} className={`${compact ? "" : "type-data"} ${tone} ${className}`} aria-label={name} {...rest}>
        {GLYPH[kind]}
        {count}
      </span>
    );
  }
  if (kind === "live") {
    return (
      <span ref={ref} role={role} className={`type-data text-ink ${className}`} {...rest}>
        <span aria-hidden className="text-live">
          {GLYPH.live}
        </span>{" "}
        {words}
      </span>
    );
  }
  return (
    <span ref={ref} role={role} className={`${inherit ? "" : "type-data"} ${tone} ${className}`} {...rest}>
      {`${GLYPH[kind]} ${words}`}
    </span>
  );
});
