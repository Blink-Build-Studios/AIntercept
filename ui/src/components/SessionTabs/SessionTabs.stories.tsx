import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { sessionTabs } from "../ProviderMark/providerSpecimens";
import { SessionTabs } from "./SessionTabs";
import { tabHints } from "./TabHint";

const meta = { title: "Components/Controls/SessionTabs", component: SessionTabs, parameters: { layout: "padded" } } satisfies Meta<typeof SessionTabs>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Prompts active: counts with their units, and the hint for the active tab. */
export const WithHints: Story = {
  args: { tabs: sessionTabs, value: "prompts", onChange: fn(), hint: tabHints.prompts("Sam", "one prompt, its 203 model calls as steps", "Codex calls a prompt a turn.") },
};
/** Requests active: the hint says what the wire holds. */
export const RequestsActive: Story = { args: { tabs: sessionTabs, value: "requests", onChange: fn(), hint: tabHints.requests(["limits", "catalog", "metrics"]) } };
