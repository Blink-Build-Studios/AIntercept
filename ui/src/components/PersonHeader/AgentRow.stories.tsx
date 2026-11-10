import type { Meta, StoryObj } from "@storybook/react";
import { personHeaders } from "../ProviderMark/providerSpecimens";
import { AgentRow } from "./AgentRow";
import { PersonHeaderBlock } from "./PersonHeaderRow";

const meta = {
  title: "Components/Providers/AgentRow",
  component: AgentRow,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ width: 1344 }}>
        <PersonHeaderBlock>{Story()}</PersonHeaderBlock>
      </div>
    ),
  ],
} satisfies Meta<typeof AgentRow>;
export default meta;
type Story = StoryObj<typeof meta>;

const [claudeCode, codex] = personHeaders.mixed.agentRows;

/** Codex: Plan, 5h window, Week (resets under each), Credits. */
export const Codex: Story = { args: codex! };
/** Claude Code: Plan, Organization, Role, Using since, 7-day limit, Flags, Bypass, Missed. */
export const ClaudeCode: Story = { args: claudeCode! };
