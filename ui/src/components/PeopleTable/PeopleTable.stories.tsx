import type { Meta, StoryObj } from "@storybook/react";
import { PersonRow } from "../PersonRow/PersonRow";
import { PEOPLE_NOW, peopleRows, peopleViewRows } from "../ProviderMark/providerSpecimens";
import { PeopleTable } from "./PeopleTable";

const meta = { title: "Components/Rows/PeopleTable", component: PeopleTable, parameters: { layout: "padded" } } satisfies Meta<typeof PeopleTable>;
export default meta;
type Story = StoryObj<typeof meta>;

/** All agents, the default: only the columns every provider fills. */
export const AllAgents: Story = {
  args: { view: "all" },
  render: (args) => (
    <PeopleTable {...args}>
      {[peopleRows.mixed, peopleRows.codex, peopleRows.chat].map((p) => (
        <PersonRow key={p.personId} person={p} href="#" now={PEOPLE_NOW} activityMax={19} view="all" />
      ))}
    </PeopleTable>
  ),
};
/** Scoped to Codex: `5h · week` and Plan appended before the chevron. */
export const CodexView: Story = {
  args: { view: "codex" },
  render: (args) => (
    <PeopleTable {...args}>
      <PersonRow person={peopleRows.codex} href="#" now={PEOPLE_NOW} activityMax={19} view="codex" limits={[12, 31]} plan="Plus" />
      <PersonRow person={peopleViewRows.codexLinked} href="#" now={PEOPLE_NOW} activityMax={19} view="codex" limits={[41, 83]} plan="Plus" />
    </PeopleTable>
  ),
};
/** Scoped to Claude Code: Bypass and Missed appended. */
export const ClaudeCodeView: Story = {
  args: { view: "claude-code" },
  render: (args) => (
    <PeopleTable {...args}>
      <PersonRow person={peopleViewRows.claudeCodeLinked} href="#" now={PEOPLE_NOW} activityMax={19} view="claude-code" />
    </PeopleTable>
  ),
};
