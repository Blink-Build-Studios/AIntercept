import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import many from "../../api/samples/overview/many";
import { AgentCard, agentSummary } from "./AgentCard";
import { agentCards } from "../ProviderMark/providerSpecimens";

const meta = {
  title: "Components/Summary/AgentCard",
  component: AgentCard,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 299 }}>{Story()}</div>],
} satisfies Meta<typeof AgentCard>;
export default meta;
type Story = StoryObj<typeof meta>;

const card = (i: number) => {
  const a = many.data.agents[i];
  if (!a) throw new Error("sample has no such agent");
  return { agent: a.agent, people: a.people, summary: agentSummary(a), selected: false, onClick: fn() };
};
export const Default: Story = { args: card(0) };
export const Hover: Story = { args: { ...card(0), "data-force": "hover" } };
/** Filtering the table to this agent. */
export const Selected: Story = { args: { ...card(0), selected: true } };
export const Focus: Story = { args: { ...card(0), "data-force": "focus" } };
/** A reserved agent with nothing parsed yet. */
export const NoData: Story = { args: card(1) };

/** The agent's headline (§11L.1): Claude Code's missed calls and bypass. */
export const Extras: Story = { args: { ...agentCards[0]!, onClick: fn() } };
