// Parity specimens for the context lane's hover (CompTimelineHover.dc.html: ContextTrack/*).
// The context lane is a level BarTrack; it renders the shared timeline sheet like the other Canvas/Timeline/* stories.
import type { Meta, StoryObj } from "@storybook/react";
import { TimelineCanvasSheet } from "./Timeline.canvas.stories";

const meta = { title: "Canvas/Timeline/ContextTrack", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <TimelineCanvasSheet /> };
