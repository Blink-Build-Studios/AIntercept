// Parity specimens: the canvas's own content for each AgentChip data-spec
// (CompChips, CompRows .dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { AgentChip } from "./AgentChip";

// `nowrap` reproduces the CompRows staging: the chip sits in a `.row` (white-space: nowrap).
// The harness measures the last AgentChip/row on CompRows (ChatGPT).
const P = ({ spec, nowrap, children }: { spec: string; nowrap?: boolean; children: ReactNode }) => (
  <div data-parity={spec} className={`inline-grid items-start p-4 ${nowrap ? "whitespace-nowrap" : ""}`}>
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/AgentChip", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="AgentChip/claudeCode"><AgentChip agent="claude-code" /></P>
      <P spec="AgentChip/codex"><AgentChip agent="codex" /></P>
      <P spec="AgentChip/chatgpt"><AgentChip agent="chatgpt" /></P>
      <P spec="AgentChip/claudeAi"><AgentChip agent="claude-ai" /></P>
      <P spec="AgentChip/row" nowrap><AgentChip agent="chatgpt" /></P>
    </div>
  ),
};
