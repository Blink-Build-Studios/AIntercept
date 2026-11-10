// Parity specimens: the canvas's own content for each RangeReadout data-spec
// (CompTimeline.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { RangeReadout } from "./RangeReadout";

// The readouts stretch across the sheet's 1100px column. Times are local, as the readout shows them.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start" style={{ gridTemplateColumns: "1100px" }}>
    {children}
  </div>
);
const local = (d: number, h: number, m = 0, month = 10) => new Date(2026, month, d, h, m).toISOString();
const noop = () => {};

const meta = { title: "Canvas/Timeline/RangeReadout", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3 p-4">
      <P spec="RangeReadout/range">
        <RangeReadout mode="range" range={{ from: local(3, 10), to: local(3, 12) }} counts={{ prompts: 4, modelCalls: 96, events: 2412 }} onPrev={noop} onNext={noop} onClear={noop} />
      </P>
      {/* CompDensity (round 10): the readout's clear button, last (§11G.A8). */}
      <P spec="RangeReadout/withClear">
        <RangeReadout mode="range" range={{ from: local(3, 10), to: local(3, 12, 30) }} counts={{ prompts: 6, modelCalls: 412, events: 2104 }} onPrev={noop} onNext={noop} onClear={noop} clearLabel="Clear range" />
      </P>
      <P spec="RangeReadout/wholeSession">
        <RangeReadout mode="range" range={{ from: local(3, 10), to: local(3, 12, 30) }} counts={{ prompts: 6, modelCalls: 412, events: 2104 }} onPrev={noop} onNext={noop} onClear={noop} />
      </P>
      <P spec="RangeReadout/live">
        <RangeReadout mode="live" range={{ from: local(4, 17, 50), to: local(4, 18, 5) }} counts={{ prompts: 2, modelCalls: 19, events: 402 }} onPrev={noop} onNext={noop} onClear={noop} />
      </P>
      <P spec="RangeReadout/empty">
        <RangeReadout
          mode="empty"
          range={{ from: local(30, 2, 0, 9), to: local(30, 8, 0, 9) }}
          nearest={{ earlier: local(29, 22, 0, 9), later: local(30, 9, 0, 9) }}
          onPrev={noop}
          onNext={noop}
          onClear={noop}
        />
      </P>
    </div>
  ),
};
