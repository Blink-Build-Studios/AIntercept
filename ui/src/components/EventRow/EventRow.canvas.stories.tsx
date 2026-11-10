// Parity specimens: the canvas's own content for each EventRow data-spec
// (CompRows.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { CSSProperties, ReactNode } from "react";
import { columnVars } from "../DataTable/DataTable";
import { EVENT_COLUMNS, EVENT_MIN_WIDTH, EventJson, EventRow, type Event } from "./EventRow";

// Staging: CompRows draws each row alone (no table, so no hairline) in a 1180px
// frame with min width 1100; the compact rows in a 720px frame. The marker cell
// sizes to its glyph, so it takes no column var.
const P = ({ spec, width = 1180, children }: { spec: string; width?: number; children: ReactNode }) => (
  <div
    data-parity={spec}
    className="grid items-start"
    style={{ gridTemplateColumns: `${width}px`, "--dt-min-width": `${EVENT_MIN_WIDTH}px`, ...columnVars(EVENT_COLUMNS.slice(0, 5)) } as CSSProperties}
  >
    {children}
  </div>
);

const NOW = "2026-11-04T18:00:00";
const base: Event = {
  eventId: "evt-1",
  t: "2026-11-04T10:02:09",
  name: "tengu_tool_use_start",
  promptIndex: 17,
  linkedTo: { kind: "tool", id: "toolu_01…41", label: "Bash" },
  summary: "toolUseID toolu_…41",
  marker: null,
  internal: false,
  collapsedCount: null,
};
const E = (over: Partial<Event>): Event => ({ ...base, ...over });
const ticks = E({ t: "2026-11-04T10:02:10", name: "tengu_tool_progress", summary: "collapsed · client ticks", collapsedCount: 8 });
const marked = E({ t: "2026-11-04T10:02:37", name: "tengu_bash_tool_command_executed", summary: "rm · exit 0 · destructive_category rm -rf", marker: "destructive" });
const success = E({ name: "tengu_api_success", linkedTo: { kind: "request", id: "req_01H…a10", label: "…a10" }, summary: "costUSD 0.18 · ttftMs 2,104" });
// CompTableHover "Linked to" hover: the row hovered, its link target underlined.
const linkedTo = E({ t: "2026-11-04T10:02:09", name: "tengu_api_success", linkedTo: { kind: "request", id: "req_01H…a10", label: "…a10" }, summary: "costUSD 0.18 · ttftMs 2,104" });
const apiSuccess = E({ name: "tengu_api_success", linkedTo: { kind: "request", id: "req_01H…a10", label: "…a10" } });
const json = `{
  "requestId": "req_01H…a10",  "session_id": "4c1a…77e0",
  "costUSD": 0.18,  "ttftMs": 2104,  "permissionMode": "bypassPermissions",
  "env": { … +45 fields }
}`;

const meta = { title: "Canvas/Rows/EventRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

const Row = (p: { event: Event; expanded?: boolean; "data-force"?: "linkHover" }) => (
  <EventRow event={p.event} expanded={p.expanded} json={json} now={NOW} onToggle={fn()} onLink={fn()} data-force={p["data-force"]} />
);

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="EventRow/default"><Row event={base} /></P>
      <P spec="EventRow/collapsedTicks"><Row event={ticks} /></P>
      <P spec="EventRow/marked"><Row event={marked} /></P>
      <P spec="EventRow/selectedExpanded"><Row event={success} expanded /></P>
      <P spec="EventRow/expandedJson"><EventJson json={json} /></P>
      <P spec="EventRow/linkedToHover" width={1344}>
        <Row event={linkedTo} data-force="linkHover" />
      </P>
      <P spec="EventRow/compact" width={720}>
        <EventRow event={{ ...apiSuccess, summary: "costUSD 0.21 · ttftMs 1,912" }} compact={{ offsetMs: 9300, joinKey: "requestId", matched: true }} onToggle={fn()} />
      </P>
      <P spec="EventRow/compactNoMatch" width={720}>
        <EventRow
          event={{ ...apiSuccess, summary: "costUSD 0.17 · ttftMs 2,011 · durationMs 7,702" }}
          compact={{ offsetMs: 7700, joinKey: "requestId", matched: false }}
          onToggle={fn()}
        />
      </P>
    </div>
  ),
};
