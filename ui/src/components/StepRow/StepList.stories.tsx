import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { PROMPT_COLUMNS, PROMPT_MIN_WIDTH } from "../PromptRow/PromptRow";
import { longTurnRuns, longTurnSteps } from "../ProviderMark/providerSpecimens";
import { StepList } from "./StepList";
import { StepRow } from "./StepRow";
import { StepRun } from "./StepRun";

const meta = {
  title: "Components/Rows/StepList",
  component: StepList,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <DataTable columns={PROMPT_COLUMNS} minWidth={PROMPT_MIN_WIDTH} label="Steps" expandable>
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof StepList>;
export default meta;
type Story = StoryObj<typeof meta>;

const rows = [
  <StepRow key="1" numbered={longTurnSteps.first} />,
  <StepRow key="2" numbered={longTurnSteps.second} />,
  <StepRun key="3" {...longTurnRuns.collapsed} onToggle={fn()} />,
  <StepRow key="42" numbered={longTurnSteps.patch} />,
  <StepRun key="43" {...longTurnRuns.collapsedFailing} onToggle={fn()} />,
];

/** Past the page: `Showing steps 1–120 · show 83 more` (here the page is 4 rows, to show it). */
export const More: Story = { args: { rows, limit: 4, stepsShown: 120, totalSteps: 203, onMore: fn() } };
/** Everything fits: no link. */
export const All: Story = { args: { rows, stepsShown: 203, totalSteps: 203, onMore: fn() } };
