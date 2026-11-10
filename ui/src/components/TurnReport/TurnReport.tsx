import type { Agent } from "../AgentChip/AgentChip";
import { AgentSectionHead } from "../AgentSectionHead/AgentSectionHead";
import "./TurnReport.css";

export type TurnReportProps = {
  agent: Agent;
  /** The report's fields that the event carried, already formatted, in order: "sampling 2.4 s", "7 requests", … Absent fields are left out. */
  parts: string[];
};

/**
 * The agent's own report on a turn (handoff §11J gap 6, Codex's codex_turn_event): the
 * first line inside an expanded prompt, under the agent heading, one mono 12 line.
 */
export function TurnReport({ agent, parts }: TurnReportProps) {
  if (!parts.length) return null;
  return (
    <div role="row" className="turn-report text-ink-muted">
      <div role="cell">
        <AgentSectionHead agent={agent} topic="turn report" />
      </div>
      <span role="cell">{parts.join(" · ")}</span>
    </div>
  );
}
