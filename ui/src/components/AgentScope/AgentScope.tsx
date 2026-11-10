import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import { SegmentedControl } from "../SegmentedControl/SegmentedControl";

/** A person page's scope: all of the person's agents, or one of them. */
export type AgentScopeValue = "all" | Agent;

export type AgentScopeProps = {
  /** The person's agents, in order; the control shows only when there are two or more. */
  agents: readonly Agent[];
  value: AgentScopeValue;
  onChange: (value: AgentScopeValue) => void;
};

/**
 * The person page's agent scope (handoff §11M 3.3): `All agents · Claude Code · Codex`, each
 * agent with its dot. It scopes both the sessions table and All requests, and renders nothing
 * for a one-agent person.
 */
export function AgentScope({ agents, value, onChange }: AgentScopeProps) {
  if (agents.length < 2) return null;
  return (
    <SegmentedControl
      aria-label="Agents"
      options={[{ value: "all", label: "All agents" }, ...agents.map((a) => ({ value: a, label: AGENT_NAME[a], dot: AGENT_BG[a] }))]}
      value={value}
      onChange={(v) => onChange(v as AgentScopeValue)}
    />
  );
}
