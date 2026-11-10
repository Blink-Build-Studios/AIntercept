import type { Meta, StoryObj } from "@storybook/react";
import { mixedSessions, PEOPLE_NOW } from "../ProviderMark/providerSpecimens";
import { SessionRow } from "../SessionRow/SessionRow";
import { SessionTable } from "./SessionTable";

const meta = { title: "Rows/SessionTable", component: SessionTable, args: { view: "mixed" } } satisfies Meta<typeof SessionTable>;
export default meta;
type Story = StoryObj<typeof meta>;

const codex = mixedSessions.filter((r) => r.session.agent === "codex");

/** All agents for a linked person (§11M 2.7): only the common columns, with Agent. */
export const Mixed: Story = {
  render: () => (
    <SessionTable view="mixed" expandable>
      {mixedSessions.map((r) => (
        <SessionRow key={r.spec} session={r.session} href="#" now={PEOPLE_NOW} agentColumn={{ client: r.session.agent === "codex" ? r.session.client : null }} nesting={r.nesting} threadFacts={r.threadFacts} />
      ))}
    </SessionTable>
  ),
};
/** Scoped to Codex: the common columns without Agent. */
export const Codex: Story = {
  render: () => (
    <SessionTable view="codex" expandable>
      {codex.map((r) => (
        <SessionRow key={r.spec} session={r.session} href="#" now={PEOPLE_NOW} common nesting={r.nesting} threadFacts={r.threadFacts} />
      ))}
    </SessionTable>
  ),
};
/** Scoped to Claude Code: the canonical SessionRow, no Agent column. */
export const ClaudeCode: Story = {
  render: () => (
    <SessionTable view="claude-code">
      <SessionRow session={mixedSessions[0]!.session} href="#" now={PEOPLE_NOW} />
    </SessionTable>
  ),
};
/** Loading: skeleton rows in the scope's columns. */
export const Loading: Story = { args: { view: "mixed", loading: true } };
