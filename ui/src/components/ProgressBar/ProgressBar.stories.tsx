import type { Meta, StoryObj } from "@storybook/react";
import { ProgressBar } from "./ProgressBar";

const meta = {
  title: "Components/Interaction/ProgressBar",
  component: ProgressBar,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="relative border border-line" style={{ width: 448, height: 16 }}>{Story()}</div>],
} satisfies Meta<typeof ProgressBar>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Hidden: Story = { args: { active: false, scope: "region" } };
export const Indeterminate: Story = { args: { active: true, scope: "region" } };
/** Under prefers-reduced-motion the bar is static at full width (§6A.7). */
export const ReducedMotion: Story = { args: { active: true, scope: "region" }, parameters: { chromatic: { prefersReducedMotion: "reduce" } } };
