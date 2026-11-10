import { ThreadOrigin, type ThreadOriginData } from "../ThreadOrigin/ThreadOrigin";
import "./SessionFacts.css";

export type SessionFact = { k: string; v: string };

export type SessionFactsProps = {
  /** Where and how the session ran, in order: cwd, shell, tz (Codex's environment block), approval, sandbox. Absent facts are left out. */
  facts: SessionFact[];
  /** A fork, resume or subagent thread's origin, last. */
  origin?: ThreadOriginData | null;
};

/** The session header's facts line (handoff §11J gap 1), after the title row: key in micro caps, value in --ink-muted. */
export function SessionFacts({ facts, origin }: SessionFactsProps) {
  if (!facts.length && !origin) return null;
  return (
    <div className="session-facts text-ink-muted">
      {facts.map((f) => (
        <span key={f.k}>
          <span className="type-key text-ink-faint">{f.k}</span> {f.v}
        </span>
      ))}
      {origin && <ThreadOrigin origin={origin} />}
    </div>
  );
}
