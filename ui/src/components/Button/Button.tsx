import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Tooltip } from "../Tooltip/Tooltip";
import "./Button.css";

export type ButtonProps = {
  variant?: "default" | "ghost" | "icon";
  /** compact: 3px 10px, for a button inside a 32px bar (Banner; handoff §11A.9). */
  size?: "default" | "compact";
  /** The action is running: spinner, `loadingLabel`, aria-busy, repeat clicks ignored (§6A.2). */
  loading?: boolean;
  /** The gerund shown while loading ("Retrying…"). Default: the children. */
  loadingLabel?: string;
  disabled?: boolean;
  /** Why it's disabled. Required by §6A.2 ("never disable silently"). */
  disabledReason?: string;
  pressed?: boolean;
  href?: string;
  onClick?: () => void;
  "aria-label"?: string;
  /** Pins hover/focus/pressed for stories. */
  "data-force"?: "hover" | "focus" | "pressed";
  children: ReactNode;
  className?: string;
};

const VARIANT = { default: "", ghost: "btn-ghost", icon: "btn-icon" };

export function Button({
  variant = "default",
  size = "default",
  loading = false,
  loadingLabel,
  disabled = false,
  disabledReason,
  pressed,
  href,
  onClick,
  children,
  className = "",
  ...rest
}: ButtonProps) {
  const ref = useRef<HTMLButtonElement & HTMLAnchorElement>(null);
  // Lock the idle width so the label change doesn't move the layout (§6A.2).
  const [minWidth, setMinWidth] = useState<number>();
  useLayoutEffect(() => {
    if (!loading && ref.current) setMinWidth(ref.current.offsetWidth);
  }, [loading, children]);
  const inactive = disabled || loading;
  const classes = `btn type-label rounded-sm ${VARIANT[variant]} ${size === "compact" ? "btn-compact" : ""} ${className}`;
  const common = {
    ref,
    className: classes,
    style: minWidth && loading ? { minWidth } : undefined,
    "aria-busy": loading || undefined,
    "aria-disabled": inactive || undefined,
    "aria-pressed": pressed,
    "aria-label": rest["aria-label"],
    "data-force": rest["data-force"],
    onClick: (e: React.MouseEvent) => {
      if (inactive) {
        e.preventDefault();
        return;
      }
      onClick?.();
    },
  };
  const content = (
    <>
      {loading && (
        <svg aria-hidden className="btn-spinner" width="12" height="12" viewBox="0 0 12 12">
          <circle className="btn-spinner-track" cx="6" cy="6" r="4.5" fill="none" strokeWidth="1.5" />
          <path className="btn-spinner-arc" d="M6 1.5 A4.5 4.5 0 0 1 10.5 6" fill="none" strokeWidth="1.5" />
        </svg>
      )}
      {loading && loadingLabel ? loadingLabel : children}
    </>
  );
  // aria-disabled keeps it focusable, so the reason can show on focus too (§6B.3).
  const why = disabled && disabledReason ? { body: disabledReason } : null;
  return (
    <Tooltip kind="disabled" content={why}>
      {href ? (
        <a href={inactive ? undefined : href} {...common}>
          {content}
        </a>
      ) : (
        <button type="button" {...common}>
          {content}
        </button>
      )}
    </Tooltip>
  );
}
