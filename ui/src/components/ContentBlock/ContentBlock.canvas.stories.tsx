// Parity specimens for ContentBlock (CompTurns.dc.html): the turns sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { CompTurnsSheet } from "../TurnRow/CompTurns.canvas.stories";

const meta = { title: "Canvas/Turns/ContentBlock", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompTurnsSheet /> };
