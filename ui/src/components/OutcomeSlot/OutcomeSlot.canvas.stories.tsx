// Parity specimens for OutcomeSlot (CompTurns.dc.html): the turns sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { CompTurnsSheet } from "../TurnRow/CompTurns.canvas.stories";

const meta = { title: "Canvas/Turns/OutcomeSlot", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompTurnsSheet /> };
