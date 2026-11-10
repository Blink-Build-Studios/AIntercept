import type { HTMLAttributes, ReactNode } from "react";
import { useRovingFocus } from "../../lib/rovingFocus";
import "./HeaderStatGroup.css";

export type HeaderStatGroupProps = {
  children: ReactNode;
  label?: string;
  /** Wrapped rows sit 8px apart (the session header's one wrapping group, §11M 1.10). */
  wrapGap?: boolean;
} & Omit<HTMLAttributes<HTMLDivElement>, "className">;

/** HeaderStats split by hairlines (handoff §6.3); the ones that explain themselves are one tab stop, ←/→ between them (§6B.8). */
export function HeaderStatGroup({ children, label = "Details", wrapGap, ...rest }: HeaderStatGroupProps) {
  const roving = useRovingFocus({ group: "stats", orientation: "horizontal" });
  return (
    <div ref={roving.ref} {...roving.props} role="group" aria-label={label} {...rest} className={`header-stat-group flex flex-wrap ${wrapGap ? "header-stat-group-wrap" : ""}`}>
      {children}
    </div>
  );
}
