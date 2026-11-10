import type { Meta, StoryObj } from "@storybook/react";
import { UpdatedAgo } from "./UpdatedAgo";

const meta = { title: "Components/Chrome/UpdatedAgo", component: UpdatedAgo } satisfies Meta<typeof UpdatedAgo>;
export default meta;
type Story = StoryObj<typeof meta>;

const at = "2026-11-08T11:32:05Z";
const now = "2026-11-08T11:32:17Z";
/** The People page's freshness (§11H.4): an absolute time, so the slot never changes width. */
export const Idle: Story = { args: { at, now } };
/** A poll is running: a dot in the indicator cell (after 150 ms); the text stays. */
export const Polling: Story = { args: { at, updating: true, now, forcePolling: true } };
/** The last poll failed: ✕, keeping the last successful time. */
export const Failed: Story = { args: { at: "2026-11-08T11:31:50Z", failed: true, now } };
/** A day or more old: the date. */
export const OlderThanADay: Story = { args: { at: "2026-11-07T09:00:00Z", now } };
/** The tooltip: the relative age and the polling interval. */
export const WithTooltip: Story = { args: { at, now, open: true }, parameters: { layout: "centered" } };
