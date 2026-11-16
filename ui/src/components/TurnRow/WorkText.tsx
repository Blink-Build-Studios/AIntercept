import { Fragment } from "react";
import type { WorkPart } from "../../lib/work";

/** A work line's parts (lib/work.ts): text, and each failure count in its own --ink span (failures are work, not friction). */
export function WorkText({ parts }: { parts: readonly WorkPart[] }) {
  return (
    <>
      {parts.map((p, i) => (typeof p === "string" ? <Fragment key={i}>{p}</Fragment> : <span key={i} className="text-ink">{p.failed}</span>))}
    </>
  );
}
