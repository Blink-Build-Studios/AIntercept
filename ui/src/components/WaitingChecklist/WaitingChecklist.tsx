import type { Schemas } from "../../api/types";
import { formatDuration } from "../../lib/format";
import { formatTime } from "../../lib/time";
import "./WaitingChecklist.css";

export type WaitingStep = { status: "done" | "active" | "pending"; title: string; detail: string };

/**
 * The steps from `/status` (handoff §5.2.11): the proxy is up once its probes
 * arrive; then the first real request; then a person from it.
 */
export function waitingSteps(st: Schemas["Status"], now: Date): WaitingStep[] {
  const proxy = st.proxy === "up" || st.lastIngestAt !== null;
  const first = st.firstRequestAt !== null;
  const waited = st.waitingSince ? formatDuration(now.getTime() - Date.parse(st.waitingSince)) : null;
  return [
    { status: proxy ? "done" : "active", title: "Proxy running", detail: proxy ? "probe traffic is arriving" : "waiting for the proxy’s first probe" },
    {
      status: first ? "done" : proxy ? "active" : "pending",
      title: "First request",
      detail: first ? "done" : proxy ? `waiting${waited ? ` ${waited}` : ""} · route a machine through the proxy` : "pending",
    },
    { status: first ? "active" : "pending", title: "Person identified", detail: first ? "reading the account from the first session" : "pending" },
  ];
}

const GLYPH = { done: "✓", active: "●", pending: "○" };
const GLYPH_TONE = { done: "text-ink", active: "text-live", pending: "text-ink-faint" };

/**
 * First run, before any traffic (handoff §6.7): what is set up, what it waits
 * for, what comes next. ✓ done in ink, ● active in live, ○ pending in faint.
 */
export function WaitingChecklist({ steps, checkedAt }: { steps: WaitingStep[]; checkedAt: string }) {
  return (
    <ol aria-label={`Setup, checked at ${formatTime(checkedAt, new Date(checkedAt), true)}`} aria-live="polite" className="waiting border border-line bg-surface">
      {steps.map((s) => (
        <li key={s.title} className="waiting-step type-data">
          <span aria-hidden className={`waiting-glyph ${GLYPH_TONE[s.status]}`}>
            {GLYPH[s.status]}
          </span>
          <span className={`waiting-title ${s.status === "pending" ? "text-ink-muted" : "text-ink"}`}>{s.title}</span>
          <span className="waiting-detail flex-1 text-ink-muted">{s.detail}</span>
        </li>
      ))}
    </ol>
  );
}
