import type { Meta, StoryObj } from "@storybook/react";
import { ToolsOfferedUsed } from "./ToolsOfferedUsed";
import { personTools, sessionTools } from "./specimens";

const meta = {
  title: "Components/Summary/ToolsOfferedUsed",
  component: ToolsOfferedUsed,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 880 }}>{Story()}</div>],
} satisfies Meta<typeof ToolsOfferedUsed>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The session page, in range: Offered · Used · Calls, and how often the list changed. */
export const Session: Story = { args: { rows: sessionTools, changes: 2 } };
/** The person page, all sessions: Most used · Used / offered; unused servers folded. */
export const Person: Story = { args: { rows: personTools, scope: "person" } };
/** A tool list that never changed in range: no footer. */
export const Unchanged: Story = { args: { rows: sessionTools.slice(0, 1), changes: null } };
export const Loading: Story = { args: { rows: [], loading: true } };
