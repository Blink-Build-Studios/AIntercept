import { formatTokens } from "../../lib/format";
import { ContentRow } from "./ContentRow";
import { Notice } from "../Notice/Notice";
import { Fragment } from "react";
import { contentRow, summaryLabel, type Detail } from "./drawerData";
import { SummaryLabel } from "./SummaryLabel";
import "./ContentRow.css";
import "./SummaryView.css";

const FOOTER = {
  wire: "Headers, body and stream are in Raw. Limits, network, timing, events and all fields are in All details.",
  retained: "Full content is kept because this deployment runs in experimentation mode. In production, only the shape (sizes, paths, counts) is kept.",
};

/**
 * The drawer's Summary tab (handoff §6.6): where the call sits, a Notice for a
 * call that wasn't captured or failed, what was sent and received, and where the rest is.
 */
export function SummaryView({ detail: d, labelWidth = 240 }: { detail: Detail; /** The received labels' column: 240px in the 640px drawer, 220px in a 520px Summary column (§11L.9). */ labelWidth?: 220 | 240 }) {
  const step = d.stepIndex === null ? null : d.stepCount === null ? `step ${d.stepIndex}` : `step ${d.stepIndex} of ${d.stepCount}`;
  const attempt = d.retry ? `attempt ${d.retry.attempt}${d.retry.of ? ` of ${d.retry.of}` : ""}` : null;
  const where = [d.promptIndex === null ? null : `Prompt #${d.promptIndex}`, step, attempt].filter(Boolean).join(" · ");
  const check = d.kind === "failed" ? (d.retry?.nextStatus === 200 ? "retry succeeded" : null) : d.crossCheck.matched ? "✓ matches Claude Code’s own report" : null;
  const cached = d.keyStrip.tokens.cacheRead;
  const sentHead =
    d.kind === "notCaptured" ? "Sent · unknown" : cached ? `Sent · on a ${formatTokens(cached, "total")}-token cached prefix` : "Sent";
  const out = d.keyStrip.tokens.output;
  const recvHead =
    d.kind === "notCaptured" ? "Received · usage only" : d.kind === "failed" ? "Received · nothing" : out === null ? "Received" : `Received · ${formatTokens(out, "call")} tokens`;
  return (
    <div className="summary-view">
      {(where || check) && (
        <p className="summary-context type-data text-ink-muted">
          {where}
          {where && check ? " · " : ""}
          {check}
        </p>
      )}
      {d.notice && (
        <div className="summary-notice">
          <Notice tone={d.notice.tone} title={d.notice.title} glyph="">
            {d.notice.body}
          </Notice>
        </div>
      )}
      <h3 className="summary-head type-key text-ink-faint">{sentHead}</h3>
      {d.sent.map((r, i) => (
        <ContentRow key={i} row={contentRow(r)} />
      ))}
      <h3 className="summary-head summary-head-received type-key text-ink-faint">{recvHead}</h3>
      {/* Received blocks by name (§11L.9): a fixed label column; long names truncate, their count stays. */}
      {d.received.map((r, i) => {
        const l = summaryLabel(r);
        return (
          <Fragment key={i}>
            <SummaryLabel name={l.name} count={l.count} value={l.value} labelWidth={labelWidth} />
            {l.body && <pre className="content-row-body type-data bg-bg text-ink">{l.body}</pre>}
          </Fragment>
        );
      })}
      {d.kind === "normal" && <p className="summary-foot type-data text-ink-faint">{d.contentRetained ? FOOTER.retained : FOOTER.wire}</p>}
    </div>
  );
}
