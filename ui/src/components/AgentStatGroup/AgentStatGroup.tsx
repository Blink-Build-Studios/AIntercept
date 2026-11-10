import type { ReactNode } from "react";
import type { Agent } from "../AgentChip/AgentChip";
import { AgentSectionHead } from "../AgentSectionHead/AgentSectionHead";
import { Marker } from "../Marker/Marker";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./AgentStatGroup.css";

export type AgentStat = { label: string; value: ReactNode };

export type AgentStatGroupProps = {
  agent: Agent;
  /** The group's topic: "limits". */
  topic: string;
  /** The headline value under the agent heading: "5h 41% · week 83%". */
  value: ReactNode;
  /** A window at 100%: the value gets ▲ in --danger (handoff §11J gap 5). */
  atLimit?: boolean;
  /** The headline's explanation (Tooltip/codexLimits). */
  tip?: TooltipContent | null;
  /** The stats after it: "resets", "credits". */
  more?: AgentStat[];
};

/**
 * A provider's extras in the person header (handoff §11J screens 2, gap 5): after the
 * common stats, set off by a --line-strong rule on a --surface fill, headed by the agent.
 */
export function AgentStatGroup({ agent, topic, value, atLimit, tip, more = [] }: AgentStatGroupProps) {
  return (
    <div role="group" aria-label={`${topic} reported by the agent`} className="agent-stat-group">
      <Tooltip content={tip ?? null}>
        <div className={`header-stat agent-stat ${tip ? "header-stat-tip" : ""}`} data-roving={tip ? "" : undefined}>
          <AgentSectionHead agent={agent} topic={topic} />
          <div className="header-stat-value text-ink">
            {atLimit && (
              <>
                <Marker kind="limitReached" compact />{" "}
              </>
            )}
            {value}
          </div>
        </div>
      </Tooltip>
      {more.map((s) => (
        <div key={s.label} className="header-stat agent-stat">
          <div className="type-key text-ink-faint">{s.label}</div>
          <div className="header-stat-value text-ink">{s.value}</div>
        </div>
      ))}
    </div>
  );
}
