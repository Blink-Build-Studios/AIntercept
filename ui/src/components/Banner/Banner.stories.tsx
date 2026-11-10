import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { Banner } from "./Banner";

const meta = { title: "Components/Summary/Banner", component: Banner, parameters: { layout: "fullscreen" } } satisfies Meta<typeof Banner>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Delayed: Story = { args: { kind: "delayed", since: "2026-11-04T18:02:49Z" } };
export const RefreshFailed: Story = { args: { kind: "refreshFailed", since: "2026-11-04T18:02:49Z", retryInS: 10, onRetry: fn() } };
