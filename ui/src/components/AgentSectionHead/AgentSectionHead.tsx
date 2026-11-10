import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import "./AgentSectionHead.css";

export type AgentSectionHeadProps = {
  agent: Agent;
  /** What the extras are about: "limits", "this thread", "response", "turn report". */
  topic: string;
  /** The heading element; a section's own title is h3 (the default: a plain div). */
  as?: "div" | "h3";
};

/**
 * Marks a provider's extras (handoff §11J screens 2): the 7px agent dot and
 * `CODEX · LIMITS` in micro caps, in --ink-muted. Every agent-only place starts with it.
 */
export function AgentSectionHead({ agent, topic, as: Tag = "div" }: AgentSectionHeadProps) {
  return (
    <Tag className="agent-section-head type-key text-ink-muted">
      <span aria-hidden className={`agent-section-dot rounded-pill ${AGENT_BG[agent]}`} />
      {`${AGENT_NAME[agent]} · ${topic}`}
    </Tag>
  );
}
