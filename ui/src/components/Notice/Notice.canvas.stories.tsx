// Parity specimens: the canvas's own content for each Notice data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Notice } from "./Notice";

// The canvas specimens fill a 706px column.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={{ gridTemplateColumns: "706px" }}>
    {children}
  </div>
);

export default { title: "Canvas/Summary/Notice", parameters: { layout: "padded" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="Notice/danger">
        <Notice tone="danger" title="This call bypassed the proxy">
          Claude Code’s own analytics report a model call the proxy never saw.
        </Notice>
      </P>
      <P spec="Notice/neutral">
        <Notice tone="neutral" glyph="✕" title="Connection reset">
          Upstream reset after 0.6 s. Claude Code retried after 1.2 s; the retry succeeded.
        </Notice>
      </P>
    </div>
  ),
};
