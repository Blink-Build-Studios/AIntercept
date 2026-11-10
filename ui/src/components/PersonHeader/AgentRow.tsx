import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import { PersonStats, type PersonStatData } from "./PersonHeaderRow";
import "./PersonHeader.css";

export type AgentRowProps = {
  agent: Agent;
  /** The account under the agent's name ("acct 9c2e…41"); null when there is none (a web surface). */
  account?: string | null;
  /**
   * The agent's own stats, in its declared order (handoff §11L.3). Codex: Plan, 5h window,
   * Week (each with its reset as the sub), Credits. Claude Code: Plan, Organization (OrgStat),
   * Role, Using since, 7-day limit, Flags, Bypass, Missed. Only fields the agent reports.
   */
  stats: PersonStatData[];
};

/**
 * One agent's row in the person header (handoff §11L.3): the 200px label column holds the
 * agent dot and name (the dot, not the provider's caps: ProviderMark names it once) and the
 * account; the stats wrap within the row.
 */
export function AgentRow({ agent, account, stats }: AgentRowProps) {
  return (
    <div className="person-header-row person-agent-row">
      <div className="person-agent-label">
        <span className="person-agent-name type-data-strong text-ink">
          <span aria-hidden className={`person-agent-dot rounded-pill ${AGENT_BG[agent]}`} />
          {AGENT_NAME[agent]}
        </span>
        {account && <span className="person-agent-account text-ink-faint">{account}</span>}
      </div>
      <PersonStats stats={stats} label={AGENT_NAME[agent]} />
    </div>
  );
}
