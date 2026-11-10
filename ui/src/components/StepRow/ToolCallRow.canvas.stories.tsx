// Parity specimens: the canvas's own content for each ToolCallRow data-spec (CompRows.dc.html
// "tool call · …"), measured against the canvas by `make parity`. ToolCallRow lives in
// StepRow.tsx: the rows a model call's StepRow is followed by.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { ReactNode } from "react";
import { AgentRowsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { ToolCallRow, type ToolCall } from "./StepRow";

// Staging: CompRows draws each row alone (no table, so no hairline) in a 1180px frame;
// the row's 1100px content min width overflows it, as there.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: "1180px" }}>
    {children}
  </div>
);

const read: ToolCall = {
  command: null,
  toolUseId: "toolu_read",
  name: "Read",
  mcpServer: null,
  target: "src/auth/session.ts",
  status: "ok",
  durationMs: null,
  exitCode: null,
  resultBytes: 4100,
  destructive: null,
};
const marked: ToolCall = {
  ...read,
  name: "Bash",
  target: "npm test",
  exitCode: 1,
  resultBytes: 18000,
  durationMs: 12300,
  destructive: { category: "rm_recursive_force", scope: "outside_cwd" },
};
const running: ToolCall = { ...read, name: "Bash", target: "go test ./internal/coverage/...", status: "running", resultBytes: null, durationMs: 8200 };

const meta = { title: "Canvas/Rows/ToolCallRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="ToolCallRow/default"><ToolCallRow tool={read} onOpen={fn()} /></P>
      <P spec="ToolCallRow/marked"><ToolCallRow tool={marked} onOpen={fn()} /></P>
      <P spec="ToolCallRow/running"><ToolCallRow tool={running} onOpen={fn()} /></P>
      {/* CompAgents: Codex's exec and apply_patch (§11J tool rows). */}
      <AgentRowsSheet />
    </div>
  ),
};
