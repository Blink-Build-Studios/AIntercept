import type { HTMLAttributes, ReactNode } from "react";
import { useRovingFocus } from "../../lib/rovingFocus";
import "./HeaderStatGroup.css";

export type HeaderStatGroupProps = {
  children: ReactNode;
  label?: string;
  /** Wrapped rows sit 8px apart (the session header's one wrapping group, §11M 1.10, §11S). */
  wrapGap?: boolean;
  /** A wrapped stat moves whole: its value never breaks (CompProviders' session group, §11M 1.10). */
  noBreak?: boolean;
} & Omit<HTMLAttributes<HTMLDivElement>, "className">;

/** HeaderStats split by hairlines (handoff §6.3); the ones that explain themselves are one tab stop, ←/→ between them (§6B.8). */
export function HeaderStatGroup({ children, label = "Details", wrapGap, noBreak, ...rest }: HeaderStatGroupProps) {
  const roving = useRovingFocus({ group: "stats", orientation: "horizontal" });
  return (
    <div ref={roving.ref} {...roving.props} role="group" aria-label={label} {...rest} className={`header-stat-group flex flex-wrap ${wrapGap ? "header-stat-group-wrap" : ""} ${noBreak ? "header-stat-group-nobreak" : ""}`}>
      {children}
    </div>
  );
}
