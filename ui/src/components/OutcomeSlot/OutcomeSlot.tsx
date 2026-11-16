import { OutcomeFacts, type OutcomeFact } from "./OutcomeFacts";
import "./OutcomeSlot.css";

export type { OutcomeFact };

export type OutcomeSlotProps = {
  /** `Outcome` on the session page, `This fortnight` (the window) on the person page. */
  title: string;
  /** What we capture; left out when empty. */
  facts: readonly OutcomeFact[];
};

/**
 * Where a generated summary will go (handoff §11R, CompTurns OutcomeSlot): a dashed, faintly
 * hatched box tagged `needs a generated summary · not captured yet`, two skeleton lines for
 * the text, then the facts we do capture. Session: under the header. Person: under the
 * header and posture chips, above the coverage line.
 */
export function OutcomeSlot({ title, facts }: OutcomeSlotProps) {
  return (
    <section className="outcome-slot" aria-label={title}>
      <div className="type-key text-ink-muted">
        {`${title} · `}
        <span className="text-focus">needs a generated summary · not captured yet</span>
      </div>
      <div className="outcome-lines" aria-hidden>
        <div className="outcome-line outcome-line-long" />
        <div className="outcome-line outcome-line-short" />
      </div>
      <OutcomeFacts facts={facts} />
    </section>
  );
}
