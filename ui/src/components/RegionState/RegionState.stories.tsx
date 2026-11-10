import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { EmptyState } from "../EmptyState/EmptyState";
import { Skeleton } from "../Skeleton/Skeleton";
import { RegionState } from "./RegionState";

const content = (
  <ul className="flex flex-col gap-1 type-data text-ink">
    <li>Session 69ec40e4 · 640 calls</li>
    <li>Session 5e4580a7 · 4 calls</li>
  </ul>
);
const skeleton = (
  <div className="flex flex-col gap-1">
    <Skeleton shape="row" />
    <Skeleton shape="row" />
  </div>
);

const meta = {
  title: "Components/Interaction/RegionState",
  component: RegionState,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 512 }}>{Story()}</div>],
  args: { label: "Sessions", skeleton, children: content, hasData: true, status: "ready" },
} satisfies Meta<typeof RegionState>;
export default meta;
type Story = StoryObj<typeof meta>;

export const FirstLoad: Story = { args: { status: "loading", hasData: false } };
export const Refetching: Story = { args: { status: "refetching" } };
export const Slow: Story = { args: { status: "refetching", slow: { reason: "the timeline", size: "2 days · 21,305 events", onCancel: fn() } } };
export const ErrorWithData: Story = {
  args: { status: "error", error: { message: "Couldn't refresh sessions", queryId: "q_3f9a0c1e2b7d", onRetry: fn() } },
};
export const ErrorEmpty: Story = {
  args: { status: "error", hasData: false, error: { message: "Couldn't load sessions", queryId: "q_3f9a0c1e2b7d", onRetry: fn() } },
};
export const Empty: Story = { args: { status: "empty", empty: <EmptyState title="No sessions in this range" /> } };
export const Ready: Story = {};
