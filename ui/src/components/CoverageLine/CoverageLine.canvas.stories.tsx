// Parity specimens: the canvas's own content for each CoverageLine data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { CoverageLine } from "./CoverageLine";

// The canvas specimens span the 1344px sheet column. Times are local, as the viewer sees them.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start" style={{ gridTemplateColumns: "1344px" }}>
    {children}
  </div>
);
const at = (d: number, h: number, m: number) => new Date(2026, 10, d, h, m).toISOString();
const now = new Date(2026, 10, 5, 12, 0);

export default { title: "Canvas/Summary/CoverageLine", parameters: { layout: "fullscreen" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      {/* CompTableHover: the gap line hovered (its tooltip is Tooltip/coverageLine). */}
      <P spec="CoverageLine/hover">
        <CoverageLine
          now={now}
          onView={() => {}}
          data-force="hover"
          coverage={{
            reported: 734,
            captured: 640,
            notCaptured: 94,
            windows: [
              { from: at(3, 0, 0), to: at(3, 0, 59), count: 37 },
              { from: at(3, 15, 0), to: at(3, 17, 59), count: 57, cause: "IPv6 route bypassed the proxy" },
            ],
          }}
        />
      </P>
      <P spec="CoverageLine/complete">
        <CoverageLine coverage={{ reported: 22, captured: 22, notCaptured: 0, windows: [] }} />
      </P>
      <P spec="CoverageLine/gap">
        <CoverageLine
          now={now}
          onView={() => {}}
          coverage={{
            reported: 734,
            captured: 640,
            notCaptured: 94,
            windows: [
              { from: at(3, 0, 0), to: at(3, 0, 59), count: 37 },
              { from: at(3, 15, 0), to: at(3, 17, 59), count: 57 },
            ],
          }}
        />
      </P>
    </div>
  ),
};
