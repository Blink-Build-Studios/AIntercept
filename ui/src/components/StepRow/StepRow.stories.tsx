import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import demo from "../../api/samples/session_prompts/demo";
import inFlight from "../../api/samples/session_prompts/in_flight";
import many from "../../api/samples/session_prompts/many";
import one from "../../api/samples/session_prompts/one";
import type { SessionPromptsResponse } from "../../api/types";
import { DataTable } from "../DataTable/DataTable";
import { PROMPT_COLUMNS, PROMPT_MIN_WIDTH } from "../PromptRow/PromptRow";
import { StepRow, type Step, type StepRowProps } from "./StepRow";
import { codexExec, codexModelCall, codexPatch, codexPatchChange, codexSpawn } from "../AgentSectionHead/agentSpecimens";
import { longTurnSteps, longTurnStepsNoBodies } from "../ProviderMark/providerSpecimens";

const steps = (r: SessionPromptsResponse): Step[] => r.data.flatMap((i) => (i.type === "prompt" ? i.steps : []));
const all = [...steps(demo), ...steps(many), ...steps(one), ...steps(inFlight)];
const find = (pred: (s: Step) => boolean) => all.find(pred) ?? all[0]!;

const done = find((s) => s.kind === "model" && s.status === "done" && s.tools.length === 0);
const withTool = find((s) => s.kind === "model" && s.status === "done" && s.tools.length > 0 && s.tools.every((c) => !c.destructive));
const marked = find((s) => s.tools.some((c) => c.destructive));
const live = find((s) => s.status === "streaming");
const subagentRun = steps(many).filter((s) => s.kind === "subagent");

// StepRow's props are a union (a model call, or a subagent run), so each story renders it.
const row = (props: StepRowProps): Story => ({ render: () => <StepRow {...props} /> });
const open = fn();

const meta = {
  title: "Components/Rows/StepRow",
  component: StepRow,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <DataTable expandable columns={PROMPT_COLUMNS} minWidth={PROMPT_MIN_WIDTH} label="Prompts">
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof StepRow>;
export default meta;
type Story = StoryObj;

/** A finished model call (end_turn, no tool calls): opens the drawer. */
export const ModelDone = row({ step: done, onOpen: open });
/** A model call followed by its indented tool call. Tool targets are null until M7 (`—`). */
export const Tool = row({ step: withTool, onOpen: open });
/** A destructive command: `▲ rm -f` in danger, with the non-zero exit code. */
export const ToolMarked = row({ step: marked, onOpen: open });
/** A tool still running (progress ticks, no result yet). */
export const ToolRunning = row({ step: { ...live, status: "done", eventsPending: false }, onOpen: open });
/** A model call still streaming: `● streaming`, the dot pulsing; the count appears once output tokens arrive. */
export const Streaming = row({ step: { ...live, tools: [] }, onOpen: open });
export const StreamingWithTokens = row({ step: { ...live, tools: [], tokens: { ...live.tokens, output: 214 } }, onOpen: open });
/** Captured, but tengu_api_success not joined yet: `cost — · events pending…`. */
export const EventsPending = row({ step: { ...done, eventsPending: true }, onOpen: open });
/** A subagent's run, collapsed to one `⤷` row. */
export const Subagent = row({ subagent: subagentRun, expanded: false, onToggle: fn(), onOpen: open });
export const SubagentExpanded = row({ subagent: subagentRun, expanded: true, onToggle: fn(), onOpen: open });
/** An auxiliary call (dimmed like other background calls). */
export const Auxiliary = row({ step: { ...done, requestClass: "auxiliary" }, onOpen: open });
/** A failed call: `✕ ERR` in bold ink. */
export const Failed = row({ step: find((s) => s.status === "error"), onOpen: open });
/** One level down (a subagent's calls): model 100px, tools 130px. */
export const Nested = row({ step: withTool, depth: 1, onOpen: open });
export const Selected = row({ step: withTool, selected: true, onOpen: open });
/** A Codex model call: the model as sent, reasoning tokens and effort; its exec and patch below (§11J). */
export const CodexModelCall = row({
  step: { ...codexModelCall, tools: [codexExec, codexPatch] },
  extras: { reasoning: 212, effort: "medium" },
  toolExtras: [{ approval: "approved (on-request)" }, { patch: codexPatchChange }],
  onOpen: open,
});
/** A spawned thread: its own session, linked (§11J gap 3). */
export const CodexSubagent = row({ spawn: codexSpawn });

/** A long turn's step (§11L.7): numbered, one line: model · in → out · what it produced. */
export const LongTurn: Story = row({ numbered: longTurnSteps.patch, onOpen: open });
/** Without recorded bodies (§11M 3.1): step kind and tool counts stay. */
export const LongTurnNoBodies: Story = row({ numbered: longTurnStepsNoBodies.first, onOpen: open });
/** A patch step without bodies: `apply_patch ×1`, no target or line counts. */
export const LongTurnPatchNoBodies: Story = row({ numbered: longTurnStepsNoBodies.patch, onOpen: open });
/** A text-only step without bodies reads `text`, then its stop reason. */
export const LongTurnTextNoBodies: Story = row({ numbered: longTurnStepsNoBodies.text, onOpen: open });
