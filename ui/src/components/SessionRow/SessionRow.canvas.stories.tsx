// Parity specimens: the canvas's own content for each SessionRow data-spec
// (CompRows.dc.html; CompInteraction's pendingNavigation is illustrative), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { SESSION_COLUMNS, SESSION_MIN_WIDTH, SessionRow, type Session } from "./SessionRow";
import { columnVars } from "../DataTable/DataTable";
import { AgentRowsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { ProvidersSessionsSheet } from "../ProviderMark/CompProviders.canvas.stories";

// Staging: CompRows draws each row alone (no table, so no hairline) in a 1180px
// frame, with the table's min width (the row overflows the frame, as there).
// CompTableHover draws its rows in a 1344px frame (`frame`).
const P = ({ spec, frame = 1180, children }: { spec: string; frame?: number; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: `${frame}px`, ...columnVars(SESSION_COLUMNS), "--dt-min-width": `${SESSION_MIN_WIDTH}px` } as CSSProperties}>
    {children}
  </div>
);

const NOW = "2026-11-04T10:30:00";
const base: Session = {
  agent: "claude-code",
  client: "cli",
  clientVersion: null,
  origin: null,
  children: null,
  hiddenCalls: 0,
  commit: null,
  sessionId: "4c1a0000-0000-4000-8000-0000000077e0",
  start: "2026-10-23T17:00:00",
  end: "2026-10-23T20:30:00",
  live: false,
  repo: "web",
  branch: "feat/csv",
  label: { source: "recap", text: "Added CSV export; tests pass" },
  capturedFromPrompt: null,
  prompts: 8,
  modelCalls: 282,
  events: null,
  friction: { frustrated: 0, interrupted: 0, destructive: 0 },
  bypassPct: 0,
  coverage: { captured: 282, notCaptured: 0, reported: 282, windows: [] },
  fieldsMissing: false,
};
const liveGap: Session = {
  ...base,
  start: "2026-11-02T10:04:00",
  end: null,
  live: true,
  repo: "aintercept",
  branch: "partner-readiness",
  label: { source: "recap", text: "Coverage check and IPv6 routing; PR #2 open" },
  prompts: 34,
  modelCalls: 640,
  events: 21305,
  friction: { frustrated: 1, interrupted: 1, destructive: 10 },
  bypassPct: 99.7,
  coverage: { captured: 640, notCaptured: 94, reported: 734, windows: [] },
};
const fieldsMissing: Session = {
  ...base,
  start: "2026-10-27T10:02:00",
  end: "2026-10-27T10:26:00",
  repo: null,
  branch: null,
  label: { source: "none", text: "identity and repo not parsed (pre-Nov 2)" },
  prompts: 1,
  modelCalls: 4,
  bypassPct: null,
  coverage: null,
  fieldsMissing: true,
};
const noRecap: Session = {
  ...base,
  start: "2026-10-28T19:00:00",
  end: "2026-10-28T19:48:00",
  repo: "docs",
  branch: "readme",
  label: { source: "repo", text: "docs · readme · 19:00" },
  prompts: 2,
  modelCalls: 7,
  bypassPct: 100,
  coverage: { captured: 7, notCaptured: 0, reported: 7, windows: [] },
};

const meta = { title: "Canvas/Rows/SessionRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="SessionRow/liveGap"><SessionRow session={liveGap} href="#" now={NOW} /></P>
      <P spec="SessionRow/partialCapture" frame={1342}><SessionRow session={{ ...liveGap, capturedFromPrompt: 7 }} href="#" now={NOW} /></P>
      <P spec="SessionRow/ended"><SessionRow session={base} href="#" now={NOW} /></P>
      <P spec="SessionRow/hover"><SessionRow session={base} href="#" now={NOW} data-force="hover" /></P>
      <P spec="SessionRow/fieldsMissing"><SessionRow session={fieldsMissing} href="#" now={NOW} /></P>
      <P spec="SessionRow/noRecapLabel"><SessionRow session={noRecap} href="#" now={NOW} /></P>
      {/* CompTableHover: the row hovered, pointing the timeline at its band (§6B.5). */}
      <P spec="SessionRow/hoverLinked" frame={1344}><SessionRow session={base} href="#" now={NOW} data-force="hover" /></P>
      {/* CompAgents: the Agent column and nested Codex threads (§11J screens 3, gap 3). */}
      <AgentRowsSheet />
      {/* CompProviders: the mixed sessions table (§11M 2.2, 2.3, 2.7, 3.4). */}
      <ProvidersSessionsSheet />
    </div>
  ),
};
