import type { CSSProperties } from "react";
import "./Skeleton.css";

export type SkeletonBarProps = {
  /** px, or a CSS length ("60%"); omit to fill the row (flex: 1). */
  width?: number | string;
  height?: number;
  /** The secondary bars render at 60%. */
  faint?: boolean;
  className?: string;
};

/** One static placeholder bar: skeleton fill, no shimmer. Size it like the content it stands for. */
export function SkeletonBar({ width, height = 12, faint, className = "" }: SkeletonBarProps) {
  const style: CSSProperties = { height, ...(width === undefined ? {} : { width }) };
  return <span aria-hidden className={`sk ${width === undefined ? "sk-fill" : ""} ${faint ? "sk-faint" : ""} ${className}`} style={style} />;
}

export type SkeletonProps = {
  shape: "line" | "block" | "row" | "lane";
  /** `line` only: more than one renders a paragraph; the last line is shorter and fainter. */
  lines?: number;
  className?: string;
};

/**
 * Placeholders at the content's size (handoff §6.7, CompStates):
 * - line: a 12px text line (with `lines`, the last is 70% × 8px, faint);
 * - block: a 78px stat tile;
 * - row: a table row, 92 / 160 / rest;
 * - lane: a timeline lane, 90px label and a 32px band.
 */
export function Skeleton({ shape, lines = 1, className = "" }: SkeletonProps) {
  if (shape === "line") {
    if (lines <= 1) return <SkeletonBar width="100%" className={className} />;
    return (
      <span aria-hidden className={`sk-lines ${className}`}>
        {Array.from({ length: lines }, (_, i) =>
          i < lines - 1 ? <SkeletonBar key={i} width="100%" /> : <SkeletonBar key={i} width="70%" height={8} faint />,
        )}
      </span>
    );
  }
  if (shape === "block") return <span aria-hidden className={`sk sk-block ${className}`} />;
  if (shape === "row") {
    return (
      <div aria-hidden className={`sk-row ${className}`}>
        <div className="sk-row-cells">
          <SkeletonBar width={92} />
          <SkeletonBar width={160} />
          <SkeletonBar faint />
        </div>
      </div>
    );
  }
  return (
    <div aria-hidden className={`sk-lane ${className}`}>
      <SkeletonBar width={90} />
      <SkeletonBar height={32} className="sk-lane-band" />
    </div>
  );
}
