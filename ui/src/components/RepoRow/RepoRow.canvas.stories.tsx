// Parity specimens for RepoRow (CompTurns.dc.html): the sheet's work panel, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { WorkSpecimens } from "../TurnRow/CompTurns.canvas.stories";

const meta = { title: "Canvas/Turns/RepoRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <WorkSpecimens /> };
