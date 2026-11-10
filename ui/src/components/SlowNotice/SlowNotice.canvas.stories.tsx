// Parity specimens: the canvas's own content for the SlowNotice data-spec
// (CompInteraction.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { SlowNotice } from "./SlowNotice";

const meta = { title: "Canvas/Interaction/SlowNotice", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

// The notice centres itself over its region; the wrapper stands in for the 168px region panel.
export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-8">
      <div data-parity="SlowNotice/default" className="relative" style={{ width: 438, height: 166 }}>
        <SlowNotice reason="the timeline" size="2 days · 21,305 events" onCancel={() => {}} />
      </div>
      {/* CompR11: the copy in its row form, 820px wide. */}
      <div data-parity="SlowNotice/nameOnly" className="grid" style={{ width: 820 }}>
        <SlowNotice reason="the timeline" layout="row" onCancel={() => {}} />
      </div>
      <div data-parity="SlowNotice/nameAndSize" className="grid" style={{ width: 820 }}>
        <SlowNotice reason="the timeline" size="2 days · 21,305 events" layout="row" onCancel={() => {}} />
      </div>
    </div>
  ),
};
