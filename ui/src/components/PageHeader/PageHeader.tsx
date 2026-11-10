import type { ReactNode } from "react";

export type PageHeaderProps = {
  /** The page's h1; omit it when something above names the page (a linked person's PersonIdentity). */
  title?: string;
  subtitle?: ReactNode;
  stats?: ReactNode;
  posture?: ReactNode;
};

/** The page's name, ids, stats and posture chips (handoff §6.7). The title is the page's h1. */
export function PageHeader({ title, subtitle, stats, posture }: PageHeaderProps) {
  return (
    <div className="flex flex-col gap-3 px-8 py-6">
      <div className="flex flex-wrap items-end gap-6">
        {title !== undefined && (
          <div className="flex flex-col">
            <h1 tabIndex={-1} className="type-title text-ink">
              {title}
            </h1>
            {subtitle && <p className="type-data text-ink-faint">{subtitle}</p>}
          </div>
        )}
        {stats}
      </div>
      {posture && <div className="flex flex-wrap gap-2">{posture}</div>}
    </div>
  );
}
