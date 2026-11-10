// Parity specimens for Lane (CompTimeline.dc.html): the whole sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { TimelineCanvasSheet } from "./Timeline.canvas.stories";

const meta = { title: "Canvas/Timeline/Lane", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <TimelineCanvasSheet /> };
