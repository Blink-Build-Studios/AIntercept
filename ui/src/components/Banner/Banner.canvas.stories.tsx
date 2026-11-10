// Parity specimens: the canvas's own content for each Banner data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Banner } from "./Banner";

// The canvas specimens span the 1344px sheet column. Times are local, as the viewer sees them.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start" style={{ gridTemplateColumns: "1344px" }}>
    {children}
  </div>
);
const now = new Date(2026, 10, 5, 11, 29, 10);

export default { title: "Canvas/Summary/Banner", parameters: { layout: "fullscreen" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="Banner/delayed"><Banner kind="delayed" since={new Date(2026, 10, 5, 11, 27, 4).toISOString()} now={now} /></P>
      <P spec="Banner/refreshFailed">
        <Banner kind="refreshFailed" since={new Date(2026, 10, 5, 11, 27, 40).toISOString()} retryInS={10} onRetry={() => {}} now={now} />
      </P>
    </div>
  ),
};
