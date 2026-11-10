import type { Meta, StoryObj } from "@storybook/react";
import { CopyButton } from "./CopyButton";

const meta = {
  title: "Components/Interaction/CopyButton",
  component: CopyButton,
  parameters: { layout: "padded" },
  args: { label: "Copy as curl", text: () => "curl -X POST 'https://api.anthropic.com/v1/messages'" },
} satisfies Meta<typeof CopyButton>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Idle: Story = {};
/** The text is loading first (curl needs `/raw`). */
export const Copying: Story = { args: { force: "copying" } };
/** For 1.5 s after copying. */
export const Copied: Story = { args: { force: "copied" } };
/** For 3 s when the clipboard refuses. */
export const Failed: Story = { args: { force: "failed" } };
