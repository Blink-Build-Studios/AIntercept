import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { LONG_TURN_STRIP } from "../ProviderMark/providerSpecimens";
import { StepStrip } from "./StepStrip";

const meta = {
  title: "Components/Rows/StepStrip",
  component: StepStrip,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 1240 }}>{Story()}</div>],
} satisfies Meta<typeof StepStrip>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A 203-step Codex turn: exec in ink, patches, MCP calls and text-only steps; totals from the API. */
export const Default: Story = { args: { ...LONG_TURN_STRIP, onJump: fn() } };
/** Totals counted from the steps themselves. */
export const CountedTotals: Story = { args: { ...LONG_TURN_STRIP, totals: undefined, onJump: fn() } };
