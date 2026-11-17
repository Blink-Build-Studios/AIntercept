import { useState } from "react";
import type { Schemas } from "../../api/types";
import { formatCount } from "../../lib/format";
import { DetailSection } from "./DetailSection";
import { FieldList } from "./FieldList";
import { InputCompositionBar } from "../InputCompositionBar/InputCompositionBar";
import { detailRow, type Detail, type Details } from "./drawerData";
import { TimingWaterfall } from "./TimingWaterfall";
import "./DetailsView.css";

const UNAVAILABLE = {
  notCaptured: "Only Claude Code’s own report exists for this call. Proxy timing, headers, account limits and network details are unavailable.",
  failed: "No response, so no usage, limits or stream.",
};

/** One tool call (§11S): its input (from the response) and result (the next request); no analytics column. */
function ToolBlock({ tool: t }: { tool: Schemas["ToolCallDetail"] }) {
  const cols = [
    { h: "Input (from response)", rows: t.input },
    { h: "Result (next request)", rows: t.result },
  ];
  return (
    <section aria-label={`Tool call ${t.name}`} className="tool-block border border-line">
      <header className="tool-head bg-surface-raised">
        <span className="type-data-strong text-ch-skills">{t.name}</span>
        <span className="type-data text-ink-muted">{[t.toolUseId, t.mcpServer ? `MCP · ${t.mcpServer}` : "built-in"].filter(Boolean).join(" · ")}</span>
      </header>
      <div className="tool-cols">
        {cols.map((c) => (
          <div key={c.h} className="tool-col">
            <h4 className="type-key text-ink-faint tool-col-head">{c.h}</h4>
            {c.rows.length === 0 ? (
              <p className="type-data text-ink-muted">— not retained</p>
            ) : (
              <dl className="tool-col-facts">
                {c.rows.map((r) => {
                  const row = detailRow(r);
                  return (
                    <div key={r.k} className="tool-fact type-data">
                      <dt className="tool-fact-key text-ink-muted">{row.k}</dt>
                      <dd className={`tool-fact-value ${row.tone === "muted" ? "text-ink-muted" : "text-ink"}`}>{row.v}</dd>
                    </div>
                  );
                })}
              </dl>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}

/**
 * The drawer's All details tab (handoff §6.6, §11S): input composition, the
 * sections two to a row, each tool call, the proxy's timing waterfall and every
 * field (filterable). Nothing from the agent's own analytics.
 */
export function DetailsView({ detail: d, details: x }: { detail: Detail; details: Details }) {
  const [filter, setFilter] = useState("");
  if (d.kind !== "normal") {
    return (
      <div className="details-view">
        <p className="details-unavailable type-body text-ink-muted">{UNAVAILABLE[d.kind]}</p>
      </div>
    );
  }
  return (
    <div className="details-view">
      <div className="details-composition">
        <InputCompositionBar tokens={x.composition} />
      </div>
      <div className="details-grid">
        {x.sections.map((s) => (
          <DetailSection key={s.title} title={s.title} accent={s.accent} rows={s.rows.map(detailRow)} className="details-cell" />
        ))}
      </div>
      {x.tool.length > 0 && (
        <div className="details-tools">
          {x.tool.map((t, i) => (
            <ToolBlock key={t.toolUseId ?? i} tool={t} />
          ))}
        </div>
      )}
      <h3 className="details-head type-key text-ink-faint">Timing · proxy waterfall</h3>
      <TimingWaterfall phases={x.timing.proxy} />
      <h3 className="details-head type-key text-ink-faint">All fields · {formatCount(x.fields.length)}</h3>
      <FieldList fields={x.fields} filter={filter} onFilter={setFilter} />
    </div>
  );
}
