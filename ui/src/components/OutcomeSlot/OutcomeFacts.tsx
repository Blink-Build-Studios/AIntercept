import { Fragment } from "react";

/** One fact: text with its names in bold (`2 commits on **partner-readiness** (a41f → 9c02)`). */
export type OutcomeFact = readonly (string | { strong: string })[];

/**
 * The facts the wire carries about what a session or a fortnight did (handoff §11R, §11S;
 * CompTurns OutcomeSlot). Content-free: turns · steps · tool calls with the top tools, then
 * subagents. With content: commits and the head range, files edited by type, the last test
 * outcome, PR. Joined with ` · `; zero facts are left out by whoever builds the list.
 */
export function OutcomeFacts({ facts }: { facts: readonly OutcomeFact[] }) {
  if (!facts.length) return null;
  return (
    <div className="outcome-facts text-ink">
      {facts.map((f, i) => (
        <Fragment key={i}>
          {i > 0 && " · "}
          {f.map((part, k) => (typeof part === "string" ? <Fragment key={k}>{part}</Fragment> : <b key={k}>{part.strong}</b>))}
        </Fragment>
      ))}
    </div>
  );
}
