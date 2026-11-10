import type { Agent } from "../AgentChip/AgentChip";
import { ProviderMark } from "../ProviderMark/ProviderMark";
import { AgentRow, type AgentRowProps } from "./AgentRow";
import { PersonHeaderBlock, PersonHeaderRow, type PersonStatData } from "./PersonHeaderRow";
import "./PersonHeader.css";

export type PersonHeaderProps = {
  /** The person's agents, in the order the person first used them (ProviderMark and the rows follow it). */
  agents: Agent[];
  /** How the accounts were linked ("email"); null for one account. */
  linkedBy?: string | null;
  name: string;
  /** "sam@acme.dev · 1 account" (handoff §11L.4: `email · N accounts`; org kind is gone). */
  subtitle?: string | null;
  /** The All agents row: Sessions, Prompts, Tokens, Devices, Last seen. */
  common: PersonStatData[];
  /** One row per agent, in `agents` order. */
  agentRows: AgentRowProps[];
};

/** "chris@acme.dev · 2 accounts"; the email, or just the accounts when none was seen. */
export function personSubtitle(email: string | null, accounts: number): string {
  const n = `${accounts} ${accounts === 1 ? "account" : "accounts"}`;
  return email ? `${email} · ${n}` : n;
}

/**
 * A person page's header (handoff §11L.3): the ProviderMark, the name and its subtitle,
 * then one bordered block of rows: All agents (the common stats), then an AgentRow per
 * agent. Groups never sit side by side; each row's stats wrap within it, so a one-agent
 * and a linked person read alike at any width.
 */
export function PersonHeader({ agents, linkedBy, name, subtitle, common, agentRows }: PersonHeaderProps) {
  return (
    <header className="person-header-box">
      <ProviderMark agents={agents} scope="person" linkedBy={linkedBy} />
      <div className="person-header-title">
        <h1 className="type-title text-ink">{name}</h1>
        {subtitle && <span className="type-data text-ink-muted">{subtitle}</span>}
      </div>
      <PersonHeaderBlock>
        <PersonHeaderRow stats={common} />
        {agentRows.map((r) => (
          <AgentRow key={r.agent} {...r} />
        ))}
      </PersonHeaderBlock>
    </header>
  );
}
