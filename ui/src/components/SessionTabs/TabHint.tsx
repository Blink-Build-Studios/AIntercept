import "./SessionTabs.css";

/** The active tab's explainer: Prompts, the person's own words; Requests, the wire. */
export const tabHints = {
  /** "What Sam asked, and the work each message started: one prompt, its 203 model calls as steps. Codex calls a prompt a turn." */
  prompts: (person: string, detail: string, agentWord?: string | null) =>
    [`What ${person} asked, and the work each message started${detail ? `: ${detail}` : ""}.`, agentWord].filter(Boolean).join(" "),
  /** "Every call on the wire for this session, in time order, including calls no prompt started (limits, catalog, metrics)." */
  requests: (unprompted: string[]) =>
    `Every call on the wire for this session, in time order, including calls no prompt started${unprompted.length ? ` (${unprompted.join(", ")})` : ""}.`,
};

/**
 * One always-on line describing the active tab (handoff §11L.7): mono 11, --ink-faint, no
 * dismiss. Under the session tabs it carries their bottom padding.
 */
export function TabHint({ children, id }: { children: string; id?: string }) {
  return (
    <p id={id} className="tab-hint text-ink-faint">
      {children}
    </p>
  );
}
