// Parity specimens: the canvas's own content for each RequestRow data-spec
// (CompRows.dc.html; CompInteraction's newFlash frames are illustrative), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { CSSProperties, ReactNode } from "react";
import { columnVars } from "../DataTable/DataTable";
import { RequestRow, requestColumns, requestMinWidth, type Request } from "./RequestRow";
import { ProvidersPersonRequestsSheet, ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";

// Staging: CompRows draws each person-scope row alone (no table, so no hairline)
// in a 1180px frame, with the table's min width (the row overflows the frame, as there).
// CompTableHover draws session-scope rows (and one person-scope row) in a 1344px frame.
const P = ({ spec, scope = "person", frame = 1180, children }: { spec: string; scope?: "person" | "session"; frame?: number; children: ReactNode }) => (
  <div
    data-parity={spec}
    className="grid items-start"
    style={
      {
        gridTemplateColumns: `${frame}px`,
        ...columnVars(requestColumns(scope === "person")),
        "--dt-min-width": `${requestMinWidth(scope === "person")}px`,
      } as CSSProperties
    }
  >
    {children}
  </div>
);

const NOW = "2026-11-04T18:00:00";
const model: Request = {
  model: null,
  effort: null,
  stepIndex: null,
  toolCalls: [],
  usageMissing: null,
  text: null,
  requestId: "req-model",
  t: "2026-11-04T10:42:16",
  kind: "model",
  agent: "claude-code",
  client: "cli",
  hiddenKind: null,
  sessionId: "7d2e41c0-demo",
  sessionLabel: "partner-readiness",
  promptIndex: 19,
  method: "POST",
  path: "/v1/messages",
  detail: "main · opus · tool_use",
  status: 200,
  durationMs: 9300,
  retries: null,
  bytesUp: 8200,
  bytesDown: 3400,
  captured: true,
  background: false,
  statusText: "OK",
  stopReason: "tool_use",
  ttftMs: 1900,
  tokens: { input: 3, cacheRead: 40800, cacheWrite: 400, cacheWrite5m: 0, cacheWrite1h: 400, output: 612, accuracy: "exact" },
  contentEncoding: "gzip",
  error: null,
};
const HOVER_NOW = "2026-11-03T18:00:00";
const hovered: Request = { ...model, t: "2026-11-03T10:42:16" };
const R = (over: Partial<Request>): Request => ({ ...model, ...over });
const rows = {
  background: R({ t: "2026-11-04T10:42:28", kind: "analytics", path: "/api/event_logging/v2/batch", detail: "96 events", durationMs: 71, bytesUp: 15000, bytesDown: 100, background: true }),
  flags: R({ t: "2026-11-04T10:04:02", kind: "flags", path: "/api/eval/sdk-…", detail: "6-hourly refresh · 3 changed", durationMs: 120, bytesUp: 1800, bytesDown: 96000 }),
  count: R({ t: "2026-11-04T10:02:05", kind: "count", path: "/v1/messages/count_tokens", detail: "", durationMs: 290, bytesUp: 38000, bytesDown: 100, background: true }),
  account: R({ t: "2026-11-04T08:31:20", kind: "account", sessionId: null, method: "GET", path: "/api/oauth/organizations/…/skills/list-skills", detail: "", durationMs: 190, bytesUp: 400, bytesDown: 22000 }),
  error: R({ t: "2026-11-04T10:03:08", detail: "main · opus · connection reset", status: "ERR", durationMs: null, retries: 1, bytesUp: 5100, bytesDown: null }),
  notCaptured: R({ t: "2026-11-04T15:22:08", method: null, status: null, durationMs: 7700, bytesUp: null, bytesDown: null, captured: false }),
};

const meta = { title: "Canvas/Rows/RequestRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

const Row = (p: { row: Request; selected?: boolean }) => (
  <RequestRow row={p.row} showSession selected={p.selected} now={NOW} onOpen={fn()} />
);
// Session scope (CompTableHover): same-day time, the prompt column.
const S = (p: { force: "hover" | "linked" | "focus" }) => <RequestRow row={hovered} now={HOVER_NOW} onOpen={fn()} data-force={p.force} />;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="RequestRow/model"><Row row={model} /></P>
      <P spec="RequestRow/backgroundQuiet"><Row row={rows.background} /></P>
      <P spec="RequestRow/flags"><Row row={rows.flags} /></P>
      <P spec="RequestRow/countTokens"><Row row={rows.count} /></P>
      <P spec="RequestRow/accountNoSession"><Row row={rows.account} /></P>
      <P spec="RequestRow/error"><Row row={rows.error} /></P>
      <P spec="RequestRow/notCaptured"><Row row={rows.notCaptured} /></P>
      <P spec="RequestRow/selected"><Row row={model} selected /></P>
      <P spec="RequestRow/hoverLinked" scope="session" frame={1344}><S force="hover" /></P>
      <P spec="RequestRow/timelineHovered" scope="session" frame={1344}><S force="linked" /></P>
      <P spec="RequestRow/focus" scope="session" frame={1344}><S force="focus" /></P>
      <P spec="RequestRow/innerTargetHover" frame={1344}>
        <RequestRow row={hovered} showSession sessionHref={() => "#"} now={NOW} onOpen={fn()} data-force="innerHover" />
      </P>
      {/* CompProviders (§11L). */}
      <ProvidersSessionSheet />
      <ProvidersPersonRequestsSheet />
    </div>
  ),
};
