import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { CODEX_REQUEST_CHANNELS, PERSON_CODEX_CHANNELS, PERSON_SHARED_CHANNELS } from "../ProviderMark/providerSpecimens";
import { RequestFilters } from "./RequestFilters";

const meta = { title: "Components/Rows/RequestFilters", component: RequestFilters, parameters: { layout: "padded" } } satisfies Meta<typeof RequestFilters>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A Codex session: its five channels, and six hidden prewarm and title calls. */
export const Codex: Story = { args: { channels: CODEX_REQUEST_CHANNELS, value: [], onChange: fn(), hidden: { count: 6, shown: false, onChange: fn() } } };
/** A channel with no calls gets no chip; the hidden calls shown. */
export const SomeEmpty: Story = {
  args: { channels: CODEX_REQUEST_CHANNELS.map((c) => (c.channel === "metrics" ? { ...c, count: 0 } : c)), value: ["model"], onChange: fn(), hidden: { count: 6, shown: true, onChange: fn() } },
};
/** A linked person's All requests in All agents (§11M 3.3): the shared channels, hidden calls from any agent. */
export const PersonAllAgents: Story = { args: { channels: PERSON_SHARED_CHANNELS, value: [], onChange: fn(), hidden: { count: 41, shown: false, onChange: fn() } } };
/** The same person scoped to Codex: Codex's own channels. */
export const PersonCodex: Story = { args: { channels: PERSON_CODEX_CHANNELS, value: [], onChange: fn() } };
