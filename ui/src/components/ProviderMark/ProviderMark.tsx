import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import "./ProviderMark.css";

export type ProviderMarkProps = {
  /** The page's agent(s): one, or a linked person's agents in the order the person first used them. */
  agents: Agent[];
  /** What the page is about. */
  scope: "person" | "session";
  /** A session's client and version ("tui 0.167", "2.1.31"); null when unknown. */
  client?: string | null;
  /** How a person's accounts were linked ("email"), shown for more than one agent. */
  linkedBy?: string | null;
};

/** The eyebrow's two runs, in caps: `CLAUDE CODE + CODEX` and `· PERSON · LINKED BY EMAIL`. */
export function providerMarkText({ agents, scope, client, linkedBy }: ProviderMarkProps): { names: string; rest: string } {
  const names = agents.map((a) => AGENT_NAME[a]).join(" + ");
  const rest = [scope, scope === "session" ? client : null, scope === "person" && linkedBy && agents.length > 1 ? `linked by ${linkedBy}` : null]
    .filter((p): p is string => !!p)
    .map((p) => `· ${p}`)
    .join(" ");
  return { names: names.toUpperCase(), rest: rest.toUpperCase() };
}

/**
 * The eyebrow directly above every person and session title (handoff §11L.5): the agent
 * dot(s) and name(s) in caps (--ink, 600), then `· PERSON` or `· SESSION · <client version>`
 * in --ink-muted; mono 11, letter-spacing .06em. The one place a page names its provider;
 * rows and sections repeat only the dot.
 */
export function ProviderMark(props: ProviderMarkProps) {
  const { names, rest } = providerMarkText(props);
  return (
    <div className="provider-mark text-ink-muted">
      {props.agents.map((a) => (
        <span key={a} aria-hidden className={`provider-mark-dot rounded-pill ${AGENT_BG[a]}`} />
      ))}
      <span className="provider-mark-names text-ink">{names}</span>
      <span>{rest}</span>
    </div>
  );
}
