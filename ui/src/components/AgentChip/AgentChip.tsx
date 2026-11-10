import type { Schemas } from "../../api/types";
import { agentTip } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import "../Chip/Chip.css";

export type Agent = Schemas["SessionRow"]["agent"];

export const AGENT_NAME: Record<Agent, string> = { "claude-code": "Claude Code", codex: "Codex", chatgpt: "ChatGPT", "claude-ai": "claude.ai" };
export const AGENT_BG: Record<Agent, string> = {
  "claude-code": "bg-agent-claude-code",
  codex: "bg-agent-codex",
  chatgpt: "bg-agent-chatgpt",
  "claude-ai": "bg-agent-claude-ai",
};

export type AgentInfo = { version: string | null; firstSeenAt: string; devices: number };
export type AgentChipProps = {
  agent: Agent;
  /** Version and first sighting, for its tooltip (§6B.6 `agentInfo`). */
  info?: AgentInfo;
  /** The client after a muted middle dot (handoff §11J gap 7): "exec", "tui", "tui 0.167". */
  client?: string;
};

/**
 * Agent identity in rows (handoff §6.2), mixed case. The AppBar has no agent chips
 * (§11L.2: the header and reserved variants are retired); a page names its provider
 * once, in its ProviderMark.
 */
export function AgentChip({ agent, info, client }: AgentChipProps) {
  return (
    <Tooltip content={info ? agentTip(AGENT_NAME[agent], info.version, info.firstSeenAt, info.devices) : null}>
      <span className="chip rounded-sm border border-line text-ink">
        <span aria-hidden className={`chip-dot rounded-pill ${AGENT_BG[agent]}`} />
        {AGENT_NAME[agent]}
        {client && <span className="chip-client text-ink-muted">{`· ${client}`}</span>}
      </span>
    </Tooltip>
  );
}
