// Parity specimens for FocusRow (CompDensity.dc.html): the timeline sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { TimelineCanvasSheet } from "./Timeline.canvas.stories";

const meta = { title: "Canvas/Timeline/FocusRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <TimelineCanvasSheet /> };
