// Parity specimens: the canvas's own content for each StepRow data-spec
// (CompRows.dc.html "step · …"; the tool calls that follow a step are ToolCallRow, in
// ToolCallRow.canvas.stories.tsx), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { ReactNode } from "react";
import { AgentRowsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { StepRow, type Step } from "./StepRow";
import { ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";

// Staging: CompRows draws each row alone (no table, so no hairline) in a 1180px frame;
// the row's 1100px content min width overflows it, as there.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: "1180px" }}>
    {children}
  </div>
);

const model: Step = {
  does: "exec",
  usageMissing: null,
  commands: [],
  patches: [],
  text: null,
  exitCodes: [],
  requestId: "req-model",
  t: "2026-11-04T10:42:16",
  kind: "model",
  model: "claude-opus-4-1",
  requestClass: "main",
  tokens: { accuracy: "exact", input: 43000, cacheRead: null, cacheWrite: null, cacheWrite1h: null, cacheWrite5m: null, output: 1200 },
  durationMs: 9300,
  ttftMs: null,
  stopReason: "tool_use",
  costUsd: 0.18,
  status: "done",
  eventsPending: false,
  agentType: null,
  tools: [],
};
const streaming: Step = {
  ...model,
  t: "2026-11-04T10:09:05",
  tokens: { ...model.tokens, input: null, output: 214 },
  durationMs: null,
  stopReason: null,
  costUsd: null,
  status: "streaming",
};
const pending: Step = {
  ...model,
  t: "2026-11-04T10:08:40",
  tokens: { ...model.tokens, input: 66200, output: 380 },
  durationMs: 3200,
  costUsd: null,
  eventsPending: true,
};
const sub = (t: string): Step => ({ ...model, requestId: `req-sub-${t}`, t, kind: "subagent", model: "claude-haiku-4-5", requestClass: "subagent", agentType: "Explore", durationMs: 3000 });
const explore = [sub("2026-11-04T10:42:31"), sub("2026-11-04T10:42:40"), sub("2026-11-04T10:42:50")];

const meta = { title: "Canvas/Rows/StepRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="StepRow/modelCall"><StepRow step={model} onOpen={fn()} /></P>
      <P spec="StepRow/streaming"><StepRow step={streaming} onOpen={fn()} /></P>
      <P spec="StepRow/eventsPending"><StepRow step={pending} onOpen={fn()} /></P>
      <P spec="StepRow/subagent"><StepRow subagent={explore} expanded={false} onToggle={fn()} onOpen={fn()} /></P>
      {/* CompAgents: a Codex model call and a spawned thread (§11J). */}
      <AgentRowsSheet />
      {/* CompProviders (§11L). */}
      <ProvidersSessionSheet />
    </div>
  ),
};
