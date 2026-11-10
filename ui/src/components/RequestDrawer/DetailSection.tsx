import { AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import { AgentSectionHead } from "../AgentSectionHead/AgentSectionHead";
import { CHANNEL_TEXT, type Channel } from "../ChannelSwatch/ChannelSwatch";
import "./DetailSection.css";

/** `muted`: a value that isn't there or isn't sent ("— not sent"); `ok`: a passed check ("✓ matches usage"). */
export type DetailRow = { k: string; v: string; tone?: "muted" | "ok" | null };

/**
 * A titled block of facts in All details (handoff §6.6); two per row there. With `agent`
 * it's that provider's section (§11J screens 2), after the common ones: the agent
 * heading, `Codex · response`, with `title` as its topic.
 */
export function DetailSection({ title, accent, agent, rows, className = "" }: { title: string; accent?: Channel | null; agent?: Agent; rows: DetailRow[]; className?: string }) {
  return (
    <section aria-label={agent ? `${AGENT_NAME[agent]} · ${title}` : title} className={`detail-section ${agent ? "detail-section-agent" : ""} ${className}`}>
      {agent ? (
        <AgentSectionHead agent={agent} topic={title} as="h3" />
      ) : (
        <h3 className={`type-key ${accent ? CHANNEL_TEXT[accent] : "text-ink-faint"} detail-section-title`}>{title}</h3>
      )}
      <dl>
        {rows.map((r) => (
          <div key={r.k} className="detail-row type-data">
            <dt className="detail-key text-ink-muted">{r.k}</dt>
            <dd className={`detail-value ${r.tone === "muted" ? "text-ink-muted" : "text-ink"}`}>{r.v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
