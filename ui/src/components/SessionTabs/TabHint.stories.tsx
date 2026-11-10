import type { Meta, StoryObj } from "@storybook/react";
import { TabHint, tabHints } from "./TabHint";

const meta = { title: "Components/Controls/TabHint", component: TabHint, parameters: { layout: "padded" } } satisfies Meta<typeof TabHint>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Prompts: Story = { args: { children: tabHints.prompts("Sam", "one prompt, its 203 model calls as steps", "Codex calls a prompt a turn.") } };
export const Requests: Story = { args: { children: tabHints.requests(["limits", "catalog", "metrics"]) } };
