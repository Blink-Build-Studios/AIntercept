import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { codexRequests, codexRequestsNoBodies, personRequests } from "../ProviderMark/providerSpecimens";
import { RequestRow } from "../RequestRow/RequestRow";
import { RequestTable } from "./RequestTable";

const meta = { title: "Components/Rows/RequestTable", component: RequestTable, parameters: { layout: "padded" } } satisfies Meta<typeof RequestTable>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A Codex session's requests: model calls by prompt and step, analytics, usage, a prewarm call shown. */
export const Codex: Story = {
  args: { scope: "session" },
  render: (args) => (
    <RequestTable {...args}>
      {codexRequests.map((r) => (
        <RequestRow key={r.spec} row={r.row} channel={r.channel} detail={r.detail} onOpen={fn()} now="2026-11-09T18:00:00" />
      ))}
    </RequestTable>
  ),
};
export const Loading: Story = { args: { scope: "session", loading: true } };
/** Without recorded bodies (§11M 3.1): tool counts, `text` for a text-only call. */
export const CodexNoBodies: Story = {
  args: { scope: "session" },
  render: (args) => (
    <RequestTable {...args}>
      {codexRequestsNoBodies.map((r) => (
        <RequestRow key={r.spec} row={r.row} channel={r.channel} detail={r.detail} onOpen={fn()} now="2026-11-09T18:00:00" />
      ))}
    </RequestTable>
  ),
};
/** A linked person's All requests in All agents (§11M 3.3): an agent-dot column, time order kept. */
export const PersonAllAgents: Story = {
  args: { scope: "person", agentDot: true },
  render: (args) => (
    <RequestTable {...args}>
      {personRequests.map((r) => (
        <RequestRow key={r.spec} row={r.row} channel={r.channel} detail={r.detail} showSession agentDot sessionHref={() => "#"} onOpen={fn()} now="2026-11-09T18:00:00" />
      ))}
    </RequestTable>
  ),
};
