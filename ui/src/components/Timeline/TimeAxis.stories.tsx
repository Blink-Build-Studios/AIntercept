import type { Meta, StoryObj } from "@storybook/react";
import { Timeline } from "./Timeline";
import { TimeAxis } from "./TimeAxis";

const meta = { title: "Components/Timeline/TimeAxis", component: TimeAxis, parameters: { layout: "padded" } } satisfies Meta<typeof TimeAxis>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The axis is drawn by Timeline; these frames show its two scales. */
export const Hours: Story = { render: () => <Timeline from="2026-11-04T09:00:00Z" to="2026-11-04T17:00:00Z" label="Hours">{null}</Timeline> };
export const Days: Story = { render: () => <Timeline from="2026-10-29T00:00:00Z" to="2026-11-05T00:00:00Z" label="Days">{null}</Timeline> };
