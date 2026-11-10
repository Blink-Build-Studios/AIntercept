import type { ReactNode } from "react";
import type { Agent } from "../AgentChip/AgentChip";
import { AgentSectionHead } from "../AgentSectionHead/AgentSectionHead";
import "./AgentPanel.css";

export type AgentPanelRow = { k: string; v: ReactNode };

export type AgentPanelProps = {
  agent: Agent;
  /** "this thread" (Codex), "this session" (Claude Code). */
  topic: string;
  /** The product's declared fields, in order; fields the session doesn't have are left out by the caller. */
  rows: AgentPanelRow[];
};

/**
 * A provider's extras on the session page (handoff §11J screens 2): one panel, last in
 * the side column, headed by the agent, one fact a row.
 */
export function AgentPanel({ agent, topic, rows }: AgentPanelProps) {
  return (
    <section aria-label={`${topic} reported by the agent`} className="agent-panel bg-surface">
      <div className="agent-panel-row agent-panel-head">
        <AgentSectionHead agent={agent} topic={topic} as="h3" />
      </div>
      <dl className="agent-panel-list">
        {rows.map((r) => (
          <div key={r.k} className="agent-panel-row type-data">
            <dt className="agent-panel-key text-ink-muted">{r.k}</dt>
            <dd className="agent-panel-value text-ink">{r.v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
