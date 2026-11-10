import { Fragment } from "react";
import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import { SkeletonBar } from "../Skeleton/Skeleton";
import "./Breadcrumb.css";

/**
 * A crumb. `agents`: the agent dot(s) of what a person or session crumb points to
 * (handoff §11L.2): one for a one-agent person, both for a linked person, the session's own.
 */
export type Crumb = { label: string | null; href?: string; agents?: Agent[] };

export type BreadcrumbProps = {
  crumbs: Crumb[];
  /** Pins hover on the crumb at this index, for stories. */
  forceHover?: number;
};

/**
 * The AppBar's trail (handoff §6.7, hover §6B.7): one run of text, links inline
 * ("people / name unknown"). Link crumbs take ink and an underline on hover. A person
 * or session crumb starts with its agent dot(s), and as the current page its name is
 * ink (§11L.2); a plain current crumb ("people") stays as it was. A crumb whose name
 * is still loading shows a skeleton word (§6A.3).
 */
export function Breadcrumb({ crumbs, forceHover }: BreadcrumbProps) {
  return (
    <nav aria-label="Breadcrumb" className="inline type-data text-ink-faint">
      {crumbs.map((c, i) => (
        <Fragment key={i}>
          {i > 0 && " / "}
          {c.agents?.map((a) => (
            <span key={a} role="img" aria-label={AGENT_NAME[a]} className={`crumb-dot rounded-pill ${AGENT_BG[a]}`} />
          ))}
          {c.label === null ? (
            <SkeletonBar width={96} className="crumb-skeleton" />
          ) : c.href ? (
            <a href={c.href} className="crumb" data-force={forceHover === i ? "hover" : undefined}>
              {c.label}
            </a>
          ) : c.agents?.length ? (
            <span className="text-ink">{c.label}</span>
          ) : (
            c.label
          )}
        </Fragment>
      ))}
    </nav>
  );
}
