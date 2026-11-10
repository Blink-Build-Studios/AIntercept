import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { PROMPT_COLUMNS, PROMPT_MIN_WIDTH } from "../PromptRow/PromptRow";
import { claudeCodeRunNoBodies, longTurnRuns, longTurnRunsNoBodies } from "../ProviderMark/providerSpecimens";
import { StepRun } from "./StepRun";

const meta = {
  title: "Components/Rows/StepRun",
  component: StepRun,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <DataTable columns={PROMPT_COLUMNS} minWidth={PROMPT_MIN_WIDTH} label="Steps" expandable>
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof StepRun>;
export default meta;
type Story = StoryObj<typeof meta>;

/** 39 command-only steps as one row: tool counts, the read-only commands, span, tokens. */
export const Collapsed: Story = { args: { ...longTurnRuns.collapsed, onToggle: fn() } };
/** A run with failures: `exit 1 ×31` in --danger. */
export const CollapsedFailing: Story = { args: { ...longTurnRuns.collapsedFailing, onToggle: fn() } };
export const Expanded: Story = { args: { ...longTurnRuns.collapsed, expanded: true, onToggle: fn() } };
/** A Codex run without recorded bodies (§11M 3.1): range, count, tool counts, span, tokens; no commands. */
export const CollapsedNoBodies: Story = { args: { ...longTurnRunsNoBodies.collapsed, onToggle: fn() } };
/** With a patch among its tool counts. */
export const RunNoBodies: Story = { args: { ...longTurnRunsNoBodies.run, onToggle: fn() } };
/** Claude Code without bodies: programs and failures from its own analytics (§11N.1). */
export const ClaudeCodeNoBodies: Story = { args: { ...claudeCodeRunNoBodies, onToggle: fn() } };
