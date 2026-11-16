import type { Meta, StoryObj } from "@storybook/react";
import { EMPTY_WORK, workLine } from "../../lib/work";
import { WorkText } from "./WorkText";

const meta = { title: "Components/Turns/WorkText", component: WorkText, parameters: { layout: "padded" } } satisfies Meta<typeof WorkText>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: {
    parts: workLine({ ...EMPTY_WORK, reads: 5, edits: 2, linesAdded: 31, linesRemoved: 9, commands: [{ program: "npm test", runs: 4, failed: 3, lastExit: 0 }] }),
  },
};
