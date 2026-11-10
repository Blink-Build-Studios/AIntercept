import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { InlineError } from "./InlineError";

const meta = {
  title: "Components/Interaction/InlineError",
  component: InlineError,
  parameters: { layout: "padded" },
} satisfies Meta<typeof InlineError>;
export default meta;
type Story = StoryObj<typeof meta>;

const bar = { message: "Couldn't refresh sessions", queryId: "q_3f9a0c1e2b7d", onRetry: fn() };

export const Default: Story = { args: bar };
export const Retrying: Story = { args: { ...bar, retrying: true } };
/** Beside the control that failed; the control is the retry (§6A.2). */
export const BesideTrigger: Story = { args: { variant: "beside", message: "Still couldn't refresh · 11:32:10" } };
