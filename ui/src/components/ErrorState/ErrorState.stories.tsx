import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import errorSample from "../../api/samples/person_sessions/error";
import { ErrorState } from "./ErrorState";

const meta = {
  title: "Components/Chrome/ErrorState",
  component: ErrorState,
  parameters: { layout: "padded" },
  args: {
    title: "Couldn't load this session",
    body: errorSample.error.message,
    meta: `query ${errorSample.error.queryId}`,
    onRetry: fn(),
    back: { label: "← Back to person", href: "#" },
  },
} satisfies Meta<typeof ErrorState>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Compact: Story = { args: { compact: true, back: undefined } };
