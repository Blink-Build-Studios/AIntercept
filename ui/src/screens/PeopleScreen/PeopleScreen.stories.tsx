import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import overviewDemo from "../../api/samples/overview/demo";
import overviewUnattributed from "../../api/samples/overview/unattributed";
import peopleDemo from "../../api/samples/people/demo";
import filtered from "../../api/samples/people/filtered_agent";
import linked from "../../api/samples/people/linked";
import waiting from "../../api/samples/status/waiting";
import { readyRegion } from "../../lib/region";
import { PeopleScreen, type PeopleScreenProps } from "./PeopleScreen";

const now = peopleDemo.meta.asOf;
const base: PeopleScreenProps = {
  window: { value: "14d", onChange: fn() },
  range: { from: new Date(Date.parse(now) - 14 * 86_400_000).toISOString(), to: now },
  updated: { at: new Date(Date.parse(now) - 12_000).toISOString(), now },
  overview: readyRegion(overviewDemo.data),
  people: readyRegion(peopleDemo),
  onRetry: fn(),
  agent: null,
  onAgent: fn(),
  q: "",
  onQ: fn(),
  onPage: fn(),
  personHref: (id) => `/people/${id}`,
  now,
};

const meta = { title: "Screens/PeopleScreen", component: PeopleScreen, parameters: { layout: "fullscreen" }, args: base } satisfies Meta<typeof PeopleScreen>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The demo person, live, with traffic the proxy missed. */
export const Default: Story = {};
/** With an unattributed session above the table. */
export const Unattributed: Story = { args: { overview: readyRegion(overviewUnattributed.data) } };
export const Loading: Story = { args: { overview: { status: "loading", data: undefined }, people: { status: "loading", data: undefined } } };
/** No request through the proxy yet: the setup checklist. */
export const Waiting: Story = { args: { status: waiting.data } };
export const Delayed: Story = {
  args: {
    banner: { kind: "delayed", since: new Date(Date.parse(now) - 4 * 60_000).toISOString(), now: new Date(now) },
  },
};
export const RefreshFailed: Story = {
  args: {
    updated: { at: new Date(Date.parse(now) - 30_000).toISOString(), failed: true, now },
    banner: { kind: "refreshFailed", since: new Date(Date.parse(now) - 30_000).toISOString(), retryInS: 10, onRetry: fn(), now: new Date(now) },
    people: { status: "error", data: peopleDemo, error: { message: "Query failed", queryId: "q_sample" } },
  },
};
/** All agents (the default) with a person linked by email: common columns only (handoff §11L.1). */
export const Linked: Story = { args: { people: readyRegion(linked) } };
/** The Claude Code card selected: its view appends Bypass and Missed. */
export const ClaudeCodeView: Story = { args: { agent: "claude-code" } };
/** The Codex card selected: its view appends `5h · week` and Plan. */
export const CodexView: Story = { args: { agent: "codex", people: readyRegion(linked) } };
/** Scoped to an agent no one in the window uses: no one matches. */
export const FilteredCodex: Story = { args: { agent: "codex", people: readyRegion(filtered) } };
