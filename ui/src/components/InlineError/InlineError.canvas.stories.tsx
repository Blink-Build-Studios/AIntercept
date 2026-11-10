// Parity specimens: the canvas's own content for each InlineError data-spec
// (CompInteraction.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { InlineError } from "./InlineError";

// `track` reproduces the canvas staging: the bar spans its region panel.
const P = ({ spec, track, children }: { spec: string; track?: number; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={track ? { gridTemplateColumns: `${track}px` } : undefined}>
    {children}
  </div>
);

const meta = { title: "Canvas/Interaction/InlineError", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="InlineError/default" track={438}>
        <InlineError message="Query timed out" queryId="q_8f…" onRetry={() => {}} />
      </P>
      <P spec="InlineError/besideTrigger">
        <InlineError variant="beside" message="Still couldn’t refresh · 11:32:10" />
      </P>
    </div>
  ),
};
