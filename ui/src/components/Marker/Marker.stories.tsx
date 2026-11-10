import type { Meta, StoryObj } from "@storybook/react";
import { Marker } from "./Marker";

const meta = { title: "Components/Chips/Marker", component: Marker, parameters: { layout: "padded" } } satisfies Meta<typeof Marker>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Frustrated: Story = { args: { kind: "frustrated" } };
export const Interrupted: Story = { args: { kind: "interrupted" } };
export const Destructive: Story = { args: { kind: "destructive", label: "rm -rf (outside cwd)" } };
export const CoverageGap: Story = { args: { kind: "coverageGap" } };
export const Match: Story = { args: { kind: "match", label: "matches Claude Code's report" } };
export const Live: Story = { args: { kind: "live", label: "streaming · 214 tokens" } };
export const Compact: Story = {
  args: { kind: "destructive", count: 16 },
  render: () => (
    <span className="inline-flex gap-3">
      <Marker kind="frustrated" count={2} />
      <Marker kind="interrupted" count={3} />
      <Marker kind="destructive" count={16} />
    </span>
  ),
};
