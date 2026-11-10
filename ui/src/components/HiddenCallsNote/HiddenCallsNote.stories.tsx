import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { HiddenCallsNote } from "./HiddenCallsNote";

const meta = { title: "Components/Agents/HiddenCallsNote", component: HiddenCallsNote, parameters: { layout: "padded" }, args: { onShow: fn() } } satisfies Meta<typeof HiddenCallsNote>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Prewarm: Story = { args: { counts: { prewarm: 1 } } };
/** A thread's first prompt: its prewarm and its title call. */
export const PrewarmAndTitle: Story = { args: { counts: { prewarm: 1, title: 1 } } };
