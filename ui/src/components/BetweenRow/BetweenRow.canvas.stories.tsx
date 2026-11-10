// Parity specimens: the canvas's own content for each BetweenRow data-spec
// (CompRows.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { columnVars } from "../DataTable/DataTable";
import { BetweenRow, type Between } from "./BetweenRow";

// Staging: CompRows draws each row alone in a 1180px frame, min width 1100, in the
// prompt rows' first columns (§11B.4: twisty 18 · # 40 · time 92), text at content width.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div
    data-parity={spec}
    className="grid items-start"
    style={
      {
        gridTemplateColumns: "1180px",
        "--dt-min-width": "1100px",
        ...columnVars([
          { id: "twisty", header: "", width: 18 },
          { id: "index", header: "#", width: 40 },
          { id: "t", header: "Time", width: 92 },
          { id: "prompt", header: "Prompt", width: 240 },
        ]),
      } as CSSProperties
    }
  >
    {children}
  </div>
);

const NOW = "2026-11-04T18:00:00"; // the rows are from Nov 3, so they carry the date
const compaction: Between = {
  kind: "compaction",
  t: "2026-11-03T11:18:00",
  detail: "compaction · 905k → 21k tokens · 2,207 messages summarized",
  requestId: null,
  tokensFrom: 905000,
  tokensTo: 21000,
  waitMs: 71000,
};
const recap: Between = { kind: "recap", t: "2026-11-03T11:02:00", detail: "recap · auxiliary · haiku · 212 chars", requestId: null, tokensFrom: null, tokensTo: null, waitMs: null };

const meta = { title: "Canvas/Rows/BetweenRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="BetweenRow/compaction"><BetweenRow between={compaction} now={NOW} /></P>
      <P spec="BetweenRow/recap"><BetweenRow between={recap} now={NOW} /></P>
    </div>
  ),
};
