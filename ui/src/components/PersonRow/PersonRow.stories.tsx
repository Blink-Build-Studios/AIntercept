import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties } from "react";
import demo from "../../api/samples/people/demo";
import many from "../../api/samples/people/many";
import { columnVars } from "../DataTable/DataTable";
import { PERSON_MIN_WIDTH, PersonRow, PersonRowSkeleton, personColumns, type Person } from "./PersonRow";
import { NOW as AGENTS_NOW, codexPerson, linkedPerson, person as claudeCodePerson } from "../AgentSectionHead/agentSpecimens";
import { PEOPLE_NOW, peopleRows, peopleViewRows } from "../ProviderMark/providerSpecimens";

const meta = {
  title: "Components/Rows/PersonRow",
  component: PersonRow,
  parameters: { layout: "padded" },
  decorators: [
    (Story, ctx) => (
      <div
        role="table"
        aria-label="PersonRow examples"
        className="grid"
        style={{ gridTemplateColumns: `${PERSON_MIN_WIDTH + 24}px`, ...columnVars(personColumns(ctx.args.view)) } as CSSProperties}
      >
        {Story()}
      </div>
    ),
  ],
  args: { person: many.data[1] as Person, href: "#", now: many.meta.asOf, activityMax: Math.max(...many.data.flatMap((p) => p.activity.map((d) => d.count))) },
} satisfies Meta<typeof PersonRow>;
export default meta;
type Story = StoryObj<typeof meta>;

const live = demo.data[0] as Person;
/** All agents, the default view (§11L.1): the common cells only. */
export const Default: Story = {};
export const Hover: Story = { args: { "data-force": "hover" } };
export const Focus: Story = { args: { "data-force": "focus" } };
export const Live: Story = { args: { person: many.data[0] as Person } };
/** The demo person in the Claude Code view: live, with calls Claude Code reported that the proxy never saw. */
export const CoverageGap: Story = { args: { person: live, now: demo.meta.asOf, view: "claude-code" } };
// No API scenario has other agents yet (only Claude Code is parsed), so these two
// take a real row and give it the agents and token accuracy those agents would have.
/** Several agents, tokens estimated: ≈. */
export const MultiAgentEstimated: Story = { args: { person: { ...live, agents: ["codex", "chatgpt"], agentInfo: [], tokensAccuracy: "estimated" } } };
/** An agent with no token counts: —. */
export const NoTokenCounts: Story = { args: { person: { ...live, agents: ["chatgpt"], agentInfo: [], tokensOut: null, tokensAccuracy: "missing" } } };
/** No name or email extracted yet: "name unknown", `email not seen` on the id line. */
export const Unidentified: Story = { args: { person: many.data[2] as Person } };
export const Loading: Story = { render: () => <PersonRowSkeleton /> };
/** One person from two accounts with the same email (§11L.1): `linked by email · 2 accounts · 3 devices`. */
export const Linked: Story = { args: { person: linkedPerson, now: AGENTS_NOW } };
/** A ChatGPT account using Codex, in the Codex view: limits and plan. */
export const CodexOnly: Story = { args: { person: codexPerson, now: AGENTS_NOW, view: "codex", limits: [12, 31], plan: "Plus" } };
export const ClaudeCodeOnly: Story = { args: { person: claudeCodePerson, now: AGENTS_NOW, view: "claude-code" } };
/** All agents (§11L.1): a linked Claude Code + Codex person, common cells only. */
export const MixedCommon: Story = { args: { person: peopleRows.mixed, now: PEOPLE_NOW, activityMax: 19 } };
/** The Codex view: `5h · week` (a window at 80%+ set apart) and Plan before the chevron. */
export const CodexView: Story = { args: { person: peopleViewRows.codexLinked, now: PEOPLE_NOW, activityMax: 19, view: "codex", limits: [41, 83], plan: "Plus" } };
/** The Claude Code view: Bypass and Missed before the chevron. */
export const ClaudeCodeView: Story = { args: { person: peopleViewRows.claudeCodeLinked, now: PEOPLE_NOW, activityMax: 19, view: "claude-code" } };
