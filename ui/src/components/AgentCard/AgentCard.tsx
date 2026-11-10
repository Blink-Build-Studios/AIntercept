import type { Schemas } from "../../api/types";
import { formatCount } from "../../lib/format";
import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import "./AgentCard.css";

export type AgentCardProps = {
  agent: Agent;
  people: number;
  /** "18 sessions · ▲ 94 calls missed", "29 sessions · 146 turns", "6 conversations". */
  summary: string;
  /**
   * The agent's headline, a faint third line (handoff §11L.1): Claude Code `bypass on 62% of
   * model calls`, Codex `limits: highest week 83%`, ChatGPT `no token counts`.
   */
  sub?: string | null;
  selected: boolean;
  onClick: () => void;
  /** Pins hover or focus, for stories. */
  "data-force"?: "hover" | "focus";
};

/** The card's second line, from `/overview`: "18 sessions · exact tokens", "≈ tokens", "no token counts", "4 conversations". */
export function agentSummary(a: Schemas["AgentOverview"]): string {
  const unit = a.shape === "conversations→messages" ? "conversation" : "session";
  const count = `${formatCount(a.sessions)} ${unit}${a.sessions === 1 ? "" : "s"}`;
  if (a.tokenAccuracy === "missing") return `${a.sessions > 0 ? `${count} · ` : ""}no token counts`;
  return `${count} · ${a.tokenAccuracy === "exact" ? "exact" : "≈"} tokens`;
}

/**
 * One agent's share of the people (handoff §6.3, §11L.1): a toggle that scopes the People
 * table to that agent (its view adds the agent's columns). Selected: ink border. Only
 * agents seen in the window get a card.
 */
export function AgentCard({ agent, people, summary, sub, selected, onClick, "data-force": force }: AgentCardProps) {
  return (
    <button type="button" aria-pressed={selected} className="agent-card type-label" data-force={force} onClick={onClick}>
      <span className="flex items-center gap-2">
        <span aria-hidden className={`agent-card-dot rounded-pill ${AGENT_BG[agent]}`} />
        <span className="type-data-strong text-ink">{AGENT_NAME[agent]}</span>
        <span className="flex-1" />
        <span className="agent-card-count text-ink-muted">{`${formatCount(people)} ${people === 1 ? "person" : "people"}`}</span>
      </span>
      <span className="type-data text-ink-muted">{summary}</span>
      {sub && <span className="agent-card-sub text-ink-faint">{sub}</span>}
    </button>
  );
}
