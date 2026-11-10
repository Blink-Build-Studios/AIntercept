import type { Meta, StoryObj } from "@storybook/react";
import { turnReportParts } from "../AgentSectionHead/agentSpecimens";
import { TurnReport } from "./TurnReport";

const meta = {
  title: "Components/Agents/TurnReport",
  component: TurnReport,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div role="table" aria-label="Prompt">{Story()}</div>],
} satisfies Meta<typeof TurnReport>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Codex: Story = { args: { agent: "codex", parts: turnReportParts } };
/** With OTel's timings, when the export carried them. */
export const WithOtel: Story = { args: { agent: "codex", parts: [...turnReportParts, "ttft 1.9 s", "ttfm 2.2 s", "e2e 2m 41s"] } };
