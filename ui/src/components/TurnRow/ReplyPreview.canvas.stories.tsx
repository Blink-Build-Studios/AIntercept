// Parity specimens for ReplyPreview (CompTurns.dc.html): the turns sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { CompTurnsSheet } from "./CompTurns.canvas.stories";

const meta = { title: "Canvas/Turns/ReplyPreview", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompTurnsSheet /> };
