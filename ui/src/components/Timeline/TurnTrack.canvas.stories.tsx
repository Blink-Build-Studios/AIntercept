// Parity specimens for TurnTrack (CompTurns.dc.html): the sheet's lanes, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { TurnsLaneSpecimens } from "../TurnRow/CompTurns.canvas.stories";

const meta = { title: "Canvas/Timeline/TurnTrack", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <TurnsLaneSpecimens /> };
