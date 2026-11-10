// Parity specimens: the canvas's own content for each PersonRow data-spec, and
// Sparkline/inRow (CompRows.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { AgentRowsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { ProvidersPeopleSheet } from "../ProviderMark/CompProviders.canvas.stories";
import { columnVars } from "../DataTable/DataTable";
import { PERSON_COLUMNS, PersonRow, PersonRowSkeleton, type Person } from "./PersonRow";

// Staging: CompRows draws each row alone (no table, so no hairline) in a 1180px
// frame; the row's 1240px min width overflows it, as there.
export const PersonParity = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: "1180px", ...columnVars(PERSON_COLUMNS), "--dt-min-width": "1240px" } as CSSProperties}>
    {children}
  </div>
);

export const NOW = new Date(2026, 10, 4, 18, 0).toISOString();
const at = (d: number, h: number, m: number) => new Date(2026, 10, d, h, m).toISOString();
const days = (counts: number[]) => counts.map((count, i) => ({ day: `2026-10-${String(21 + i).padStart(2, "0")}`, count }));
const base: Person = {
  agentLimits: [],
  personId: "p1",
  name: null,
  email: null,
  accountIdShort: "5c11…9d0",
  orgIdShort: null,
  orgKind: "personal",
  devices: 1,
  agents: ["claude-code"],
  agentInfo: [],
  linkedBy: null,
  accounts: 1,
  lastSeenAt: at(3, 2, 15),
  live: false,
  sessions: 1,
  prompts: 1,
  tokensOut: 1800,
  tokensAccuracy: "exact",
  activity: days([0, 1, 0, 2, 3, 0, 1, 0, 0, 2, 4, 1, 0, 0]),
  bypassPct: null,
  coverage: { reported: 4, captured: 4, notCaptured: 0, windows: [] },
};
export const rows: Record<string, Person> = {
  liveCoverageGap: {
    ...base,
    accountIdShort: "7d2e…a41c",
    orgKind: "unknown",
    live: true,
    lastSeenAt: NOW,
    sessions: 11,
    prompts: 66,
    tokensOut: 1_100_000,
    activity: days([0, 10, 3, 2, 1, 1, 0, 1, 1, 0, 0, 10, 10, 4]),
    bypassPct: 99.7,
    coverage: { reported: 1600, captured: 1506, notCaptured: 94, windows: [] },
  },
  multiAgentEstimated: {
    ...base,
    name: "Dana Ruiz",
    email: "dana@example.com",
    linkedBy: "email",
    agents: ["codex", "chatgpt"],
    lastSeenAt: new Date(Date.parse(NOW) - 4 * 60_000).toISOString(),
    sessions: 9,
    prompts: 74,
    tokensOut: 610_000,
    tokensAccuracy: "estimated",
    activity: days([2, 4, 0, 0, 5, 6, 3, 0, 0, 4, 7, 5, 6, 2]),
    bypassPct: null,
    coverage: null,
  },
  noTokenCounts: {
    ...base,
    accountIdShort: "",
    orgKind: "unknown",
    agents: ["chatgpt"],
    lastSeenAt: at(3, 9, 12),
    sessions: 4,
    prompts: 19,
    tokensOut: null,
    tokensAccuracy: "missing",
    activity: days([0, 1, 0, 0, 2, 0, 0, 1, 0, 0, 0, 3, 1, 0]),
    coverage: null,
  },
};

const meta = { title: "Canvas/Rows/PersonRow", parameters: { layout: "fullscreen" }, excludeStories: ["PersonParity", "NOW", "rows"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3 p-4">
      {/* CompRows draws Claude Code persons in the Claude Code view (§11L.1), with its own id lines. */}
      <PersonParity spec="PersonRow/default">
        <PersonRow person={base} href="#" now={NOW} activityMax={10} view="claude-code" idLine={ID_LINES.default} />
      </PersonParity>
      <PersonParity spec="PersonRow/hover">
        <PersonRow person={base} href="#" now={NOW} activityMax={10} view="claude-code" idLine={ID_LINES.default} data-force="hover" />
      </PersonParity>
      <PersonParity spec="PersonRow/focus">
        <PersonRow person={base} href="#" now={NOW} activityMax={10} view="claude-code" idLine={ID_LINES.default} data-force="focus" />
      </PersonParity>
      {Object.entries(rows).map(([spec, person]) => (
        <PersonParity key={spec} spec={`PersonRow/${spec}`}>
          <PersonRow person={person} href="#" now={NOW} activityMax={10} view="claude-code" idLine={ID_LINES[spec]} />
        </PersonParity>
      ))}
      <PersonParity spec="PersonRow/loading">
        <PersonRowSkeleton view="claude-code" />
      </PersonParity>
      {/* CompAgents: linked and single-agent rows (§11J screens 3, 5; superseded by CompProviders, §11L). */}
      <AgentRowsSheet />
      {/* CompProviders: the common table and the agent views (§11L.1). */}
      <ProvidersPeopleSheet />
    </div>
  ),
};

/** CompRows' own id lines where they aren't the common one (§11M 2.1: `email not seen · 1 account · 1 device`). */
const ID_LINES: Record<string, string | undefined> = {
  default: undefined,
  multiAgentEstimated: "dana@example.com · linked by email",
  noTokenCounts: "ChatGPT web · 1 device",
};
