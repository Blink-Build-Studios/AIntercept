import { forwardRef, type HTMLAttributes } from "react";
import { unknownAttrs } from "../../lib/unknown";

export type StatusCodeProps = {
  code: number | "ERR" | null;
  /** Render the status as the table cell itself, so it fills the cell as on the canvas. */
  role?: "cell";
  className?: string;
} & Omit<HTMLAttributes<HTMLSpanElement>, "role" | "className">;

/**
 * HTTP status (handoff §6.2, §11A.7): 2xx muted; errors `✕ ERR` bold ink, never red; null is "—".
 * Other props (a Tooltip's trigger handlers) pass to the span.
 */
export const StatusCode = forwardRef<HTMLSpanElement, StatusCodeProps>(function StatusCode({ code, role, className = "", ...rest }, ref) {
  if (code === null) {
    return (
      <span ref={ref} role={role} className={`type-data text-ink-muted ${className}`} {...unknownAttrs("no response")} {...rest}>
        —
      </span>
    );
  }
  const text = code === "ERR" || code >= 400 ? `✕ ${code}` : code;
  const tone = code === "ERR" || code >= 400 ? "quiet-keep type-data-strong text-ink" : "type-data text-ink-muted";
  return (
    <span ref={ref} role={role} className={`${tone} ${className}`} {...rest}>
      {text}
    </span>
  );
});
