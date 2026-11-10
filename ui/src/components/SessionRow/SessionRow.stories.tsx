import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import gap from "../../api/samples/person_sessions/coverage_gap";
import demo from "../../api/samples/person_sessions/demo";
import live from "../../api/samples/person_sessions/live";
import many from "../../api/samples/person_sessions/many";
import pre from "../../api/samples/person_sessions/pre_parser";
import { NOW as AGENTS_NOW, sessions } from "../AgentSectionHead/agentSpecimens";
import { mixedSessions, PEOPLE_NOW } from "../ProviderMark/providerSpecimens";
import { DataTable } from "../DataTable/DataTable";
import { SESSION_AGENT_COLUMNS, SESSION_AGENT_MIN_WIDTH, SESSION_COLUMNS, SESSION_MIN_WIDTH, SessionRow, type Session } from "./SessionRow";

const first = (s: { data: Session[] }) => s.data[0]!;
const meta = {
  title: "Components/Rows/SessionRow",
  component: SessionRow,
  parameters: { layout: "padded" },
  args: { session: first(demo), href: "#", now: demo.meta.asOf },
  decorators: [
    // A row with the Agent column sits in the mixed table's columns (§11J screens 3); nested threads make it a treegrid.
    (Story, { args }) =>
      args.agentColumn ? (
        <DataTable columns={SESSION_AGENT_COLUMNS} minWidth={SESSION_AGENT_MIN_WIDTH} label="Sessions" expandable={!!args.nesting}>
          {Story()}
        </DataTable>
      ) : (
        <DataTable columns={SESSION_COLUMNS} minWidth={SESSION_MIN_WIDTH} label="Sessions">
          {Story()}
        </DataTable>
      ),
  ],
} satisfies Meta<typeof SessionRow>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The real main session: live, 95 calls not captured. */
export const LiveWithGap: Story = {};
export const Ended: Story = { args: { session: first(many) } };
export const Hover: Story = { args: { session: first(gap), "data-force": "hover" } };
export const FieldsMissing: Story = { args: { session: first(pre) } };
export const NoRecap: Story = { args: { session: first(many) } };
export const SeenFrom: Story = { args: { session: first(live), now: live.meta.asOf } };
/** The timeline points at this session (its band hovered): hover fill and an ink-muted rule (§6B.5). */
export const Linked: Story = { args: { "data-force": "linked" } };

// A person with Claude Code and Codex (§11J screens 3, gap 3): the Agent column, Codex threads nested.
export const ClaudeCode: Story = { args: { session: sessions.claudeCode, now: AGENTS_NOW, agentColumn: {} } };
export const CodexParent: Story = {
  args: { session: sessions.codexParent, now: AGENTS_NOW, agentColumn: { client: "exec" }, nesting: { kind: "parent", expanded: true, onToggle: fn() }, threadFacts: "· 3 subagent · 1 review" },
};
export const CodexChild: Story = { args: { session: sessions.codexChild, now: AGENTS_NOW, agentColumn: { client: "exec" }, nesting: { kind: "child", agentName: "timer_edge_tests" } } };
export const CodexReview: Story = { args: { session: sessions.codexReview, now: AGENTS_NOW, agentColumn: { client: "exec" }, nesting: { kind: "child", agentName: "review_slugify" } } };
export const CodexFork: Story = {
  args: {
    session: sessions.codexFork,
    now: AGENTS_NOW,
    agentColumn: { client: "tui" },
    nesting: { kind: "parent", expanded: false, onToggle: fn() },
    threadFacts: "· fork of 3b9a… at #4",
    absent: { missed: true },
  },
};

const mixed = (spec: string) => mixedSessions.find((r) => r.spec === spec) ?? mixedSessions[0]!;
const mixedArgs = (spec: string) => {
  const r = mixed(spec);
  return { session: r.session, now: PEOPLE_NOW, agentColumn: { client: r.session.agent === "codex" ? r.session.client : null }, nesting: r.nesting, threadFacts: r.threadFacts };
};
/** The mixed table (§11M 2.7): only the common columns, Repo 240. */
export const MixedClaudeCode: Story = { args: mixedArgs("SessionRow/mixedClaudeCode") };
export const MixedCodexParent: Story = { args: mixedArgs("SessionRow/mixedCodexParent") };
/** A child thread, indented inside the Repo cell (§11M 2.2). */
export const MixedCodexChild: Story = { args: mixedArgs("SessionRow/mixedCodexChild") };
export const MixedCodexReview: Story = { args: mixedArgs("SessionRow/mixedCodexReview") };
/** A fork with destructive commands: the danger ▲ Marker (§11M 2.3). */
export const MixedCodexFork: Story = { args: mixedArgs("SessionRow/mixedCodexFork") };
/** An untitled thread (§11M 3.4): `untitled · thread 01a1…aed1` in --ink-faint, then its thread facts. */
export const MixedCodexUntitled: Story = { args: mixedArgs("SessionRow/mixedCodexUntitled") };
