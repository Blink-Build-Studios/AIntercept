import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import demo from "../../api/samples/session_prompts/demo";
import inFlight from "../../api/samples/session_prompts/in_flight";
import many from "../../api/samples/session_prompts/many";
import one from "../../api/samples/session_prompts/one";
import type { PromptItem, SessionPromptsResponse } from "../../api/types";
import { DataTable } from "../DataTable/DataTable";
import { PROMPT_COLUMNS, PROMPT_MIN_WIDTH, PromptRow } from "./PromptRow";
import { NOW as AGENTS_NOW, codexPrompt } from "../AgentSectionHead/agentSpecimens";
import { longTurnPrompt, longTurnPromptNoBodies } from "../ProviderMark/providerSpecimens";

const prompts = (r: SessionPromptsResponse): PromptItem[] => r.data.flatMap((i) => (i.type === "prompt" ? [i] : []));
const all = [...prompts(demo), ...prompts(many), ...prompts(one), ...prompts(inFlight)];
const find = (pred: (i: PromptItem) => boolean) => all.find(pred) ?? all[0]!;
const withSteps = find((i) => i.steps.length >= 5);
const subagentPrompt = find((i) => i.steps.some((s) => s.kind === "subagent"));

const meta = {
  title: "Components/Rows/PromptRow",
  component: PromptRow,
  parameters: { layout: "padded" },
  args: { item: find((i) => i.steps.length > 0 && !i.prompt.inFlight), expanded: false, onToggle: fn(), onOpenStep: fn(), now: demo.meta.asOf },
  decorators: [
    (Story) => (
      <DataTable expandable columns={PROMPT_COLUMNS} minWidth={PROMPT_MIN_WIDTH} label="Prompts">
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof PromptRow>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A finished prompt, not retained: its shape, steps, duration, tokens and tool tally. */
export const Collapsed: Story = {};
/** Open: surface-raised, a detail line, then its steps inline (model 70px, tools 100px). */
export const Expanded: Story = { args: { item: withSteps, expanded: true } };
/** Still running: `N steps so far · ● elapsed`, open by default and not highlighted. */
export const InFlight: Story = { args: { item: find((i) => i.prompt.inFlight), expanded: true, now: inFlight.meta.asOf } };
export const Interrupted: Story = { args: { item: find((i) => i.prompt.friction.interrupted && !i.prompt.friction.frustrated) } };
/** Frustrated `↯` (here also interrupted `✕`), beside the tool tally. */
export const Frustrated: Story = { args: { item: find((i) => i.prompt.friction.frustrated) } };
/** Destructive commands: `▲N` in danger at the end. */
export const Destructive: Story = { args: { item: find((i) => i.prompt.friction.destructive > 0 && i.steps.length > 0), expanded: true } };
/** A subagent's calls collapse into one `⤷` row; opened here. */
export const SubagentOpen: Story = {
  args: { item: subagentPrompt, expanded: true, expandedSubagents: subagentPrompt.steps.filter((s) => s.kind === "subagent").map((s) => s.requestId).slice(0, 1) },
};
/** A step open in the drawer. */
export const StepSelected: Story = { args: { item: withSteps, expanded: true, selectedRequestId: withSteps.steps[1]?.requestId ?? null } };
export const Hover: Story = { args: { "data-force": "hover" } };
/** Landed on from a timeline tick (§11H.6): selected, expanded and centred. */
export const Arrived: Story = { args: { item: withSteps, expanded: true, arrived: true } };
/** No steps captured (pre-capture prompts in the demo): analytics still give the tool tally. */
export const NoSteps: Story = { args: { item: find((i) => i.steps.length === 0) } };
/** The timeline points at this prompt (its tick hovered, §6B.5). */
export const Linked: Story = { args: { "data-force": "linked" } };
/** A Codex turn: its own tool names. */
export const CodexExpanded: Story = { args: { item: codexPrompt, expanded: true, now: AGENTS_NOW } };
/** A long Codex turn's row (§11L.7), its prompt text recorded. */
export const LongTurn: Story = { args: { item: longTurnPrompt, now: "2026-11-10T10:00:00" } };
/** Without recorded bodies (§11M 3.1): the prompt is its length, `412 chars`. */
export const LongTurnNoBodies: Story = { args: { item: longTurnPromptNoBodies, now: "2026-11-10T10:00:00" } };
