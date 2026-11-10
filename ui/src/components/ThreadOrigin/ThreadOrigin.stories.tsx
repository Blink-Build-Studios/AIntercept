import type { Meta, StoryObj } from "@storybook/react";
import { forkOrigin, resumeOrigin, subagentOrigin } from "../AgentSectionHead/agentSpecimens";
import { ThreadOrigin } from "./ThreadOrigin";

const meta = {
  title: "Components/Agents/ThreadOrigin",
  component: ThreadOrigin,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="type-data text-ink-muted">{Story()}</div>],
} satisfies Meta<typeof ThreadOrigin>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Fork: Story = { args: { origin: forkOrigin } };
export const Resume: Story = { args: { origin: resumeOrigin } };
export const Subagent: Story = { args: { origin: subagentOrigin } };
