import type { ReactNode } from "react";
import type { Agent } from "../AgentChip/AgentChip";
import { ProviderMark } from "../ProviderMark/ProviderMark";
import { SessionFacts, type SessionFact } from "../SessionFacts/SessionFacts";
import type { ThreadOriginData } from "../ThreadOrigin/ThreadOrigin";
import "./SessionHeader.css";

export type SessionHeaderProps = {
  /** Codex's generated title, Claude Code's recap or first prompt. */
  title: string;
  agent: Agent;
  /** The client and its version, for the ProviderMark: "tui 0.167", "exec 0.164", "2.1.31". */
  client?: string | null;
  /** Repo and ids, muted: "pomodoro · 3f2a1c9 · resumed from 3b9a…e21". Optional: chat surfaces have no repo. */
  sub?: string | null;
  /** A line under the title, muted (Claude Code's `Latest recap: “…”`, Session.dc.html). */
  note?: string | null;
  /** Only the agent's own facts (§11L field audit): Codex cwd, shell, tz, approval, sandbox. */
  facts?: SessionFact[];
  origin?: ThreadOriginData | null;
  /** The session's HeaderStats (a HeaderStatGroup). */
  stats?: ReactNode;
};

/**
 * A session page's header (handoff §11J gaps 1, 4, 7; §11L.5): the ProviderMark
 * (`CODEX · SESSION · TUI 0.167`, the one place the provider is named), the title and its
 * repo line, then SessionFacts and the stats.
 */
export function SessionHeader({ title, agent, client, sub, note, facts = [], origin, stats }: SessionHeaderProps) {
  return (
    <header className="session-header">
      <ProviderMark agents={[agent]} scope="session" client={client} />
      <div className="session-header-title">
        <h1 className="type-title text-ink">{title}</h1>
        {sub && <span className="type-data text-ink-muted">{sub}</span>}
      </div>
      {note && <p className="session-header-note type-body text-ink-muted">{note}</p>}
      {(facts.length > 0 || origin) && <SessionFacts facts={facts} origin={origin} />}
      {stats}
    </header>
  );
}
