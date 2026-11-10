import type { Meta, StoryObj } from "@storybook/react";
import many from "../../api/samples/person_timeline/many";
import { BandTrack } from "./BandTrack";
import { bands, d, window } from "./demoData";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";

const m = many.data;
const manyBands = m.sessions.map((s) => ({ id: s.sessionId, from: s.start, to: s.end, label: s.label, row: s.row, live: s.live }));

const meta = { title: "Components/Timeline/BandTrack", component: BandTrack, parameters: { layout: "padded" } } satisfies Meta<typeof BandTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

const frame = (from: string, to: string, height: number) => (Story: () => JSX.Element) => (
  <Timeline from={from} to={to} label="Sessions">
    <Lane label="Sessions" height={height} summary="sessions" sunken>
      {Story()}
    </Lane>
  </Timeline>
);

/** Overlapping sessions stack into rows (the "many" scenario). */
export const Default: Story = { args: { bands: manyBands, now: m.to }, decorators: [frame(m.from, m.to, 76)] };
export const PreCapture: Story = { args: { bands: bands.map((b) => ({ ...b, live: false, preCapture: true })), now: d.to }, decorators: [frame(window.from, window.to, 40)] };
/** The real main session, still live. */
export const Live: Story = { args: { bands, now: d.to }, decorators: [frame(window.from, window.to, 40)] };
export const WithInner: Story = {
  args: { bands, now: d.to, inner: () => <span className="tl-abs inset-0 bg-ch-count opacity-30" /> },
  decorators: [frame(window.from, window.to, 40)],
};
