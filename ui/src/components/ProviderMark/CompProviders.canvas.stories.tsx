// Parity specimens from CompProviders.dc.html (handoff §11L, round 14: every screen by
// provider), measured against the canvas by `make parity`. Each section is exported so the
// components it measures render it in their own Canvas story; this story renders them all
// (and is ProviderMark's).
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { CSSProperties, ReactNode } from "react";
import { ParityTags, type ParityTarget } from "../../test/ParityTag";
import { AgentCard } from "../AgentCard/AgentCard";
import { AgentScope } from "../AgentScope/AgentScope";
import { SessionRow } from "../SessionRow/SessionRow";
import { SessionTable } from "../SessionTable/SessionTable";
import { AppBar, type Crumb } from "../AppBar/AppBar";
import { PeopleTable } from "../PeopleTable/PeopleTable";
import { OrgStat, orgUnsetTip } from "../PersonHeader/OrgStat";
import { PersonHeader } from "../PersonHeader/PersonHeader";
import { PersonRow, type PeopleView, type Person } from "../PersonRow/PersonRow";
import { PromptRow } from "../PromptRow/PromptRow";
import { HeaderList } from "../RequestDrawer/HeaderList";
import { KeyStrip } from "../RequestDrawer/KeyStrip";
import { SummaryLabel } from "../RequestDrawer/SummaryLabel";
import { RequestFilters } from "../RequestFilters/RequestFilters";
import { RequestRow } from "../RequestRow/RequestRow";
import { RequestTable } from "../RequestTable/RequestTable";
import { HeaderStat } from "../HeaderStat/HeaderStat";
import { HeaderStatGroup } from "../HeaderStatGroup/HeaderStatGroup";
import { SessionHeader } from "../SessionHeader/SessionHeader";
import { SessionTabs } from "../SessionTabs/SessionTabs";
import { TabHint } from "../SessionTabs/TabHint";
import { StepList } from "../StepRow/StepList";
import { StepRow } from "../StepRow/StepRow";
import { StepRun } from "../StepRow/StepRun";
import { StepStrip } from "../StepStrip/StepStrip";
import { TooltipSurface } from "../Tooltip/Tooltip";
import { UpdatedAgo } from "../UpdatedAgo/UpdatedAgo";
import { ProviderMark } from "./ProviderMark";
import {
  CODEX_REQUEST_CHANNELS,
  PERSON_CODEX_CHANNELS,
  PERSON_SHARED_CHANNELS,
  codexRequestsNoBodies,
  mixedSessions,
  personRequests,
  LONG_TURN_STRIP,
  PEOPLE_NOW,
  agentCards,
  codexKeyTiles,
  codexRequests,
  codexResponseHeaders,
  longTurnPrompt,
  longTurnPromptNoBodies,
  longTurnRuns,
  longTurnRunsNoBodies,
  claudeCodeRunNoBodies,
  longTurnSteps,
  longTurnStepsNoBodies,
  peopleRows,
  peopleViewRows,
  personHeaders,
  sessionTabs,
  summaryLabels,
} from "./providerSpecimens";

/** A specimen in a fixed-width column (the sheet's staging width), or at its own size. */
const P = ({ spec, width, children }: { spec: string; width?: number; children: ReactNode }) => (
  <div
    data-parity={spec}
    className="grid items-start justify-items-start"
    style={width ? ({ gridTemplateColumns: `${width}px`, justifyItems: "stretch" } as CSSProperties) : undefined}
  >
    {children}
  </div>
);
/** The sheet's `.cell`: a flex column, so the specimen is a block of its own width. */
const Cell = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="flex flex-col items-start">
    {children}
  </div>
);
/** Rows in the sheet's 1344px `.frame` (1342 inside its border), each with the table's hairline. */
const Row = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="dt-grid grid items-start" style={{ gridTemplateColumns: "1342px" } as CSSProperties}>
    {children}
  </div>
);
/** The n-th data row of a DataTable (the header row is the grid's first child). */
const nthRow = (n: number) => `.dt-grid > :nth-child(${n + 1})`;

/** Issue 1: the agent cards and the People table's three views. */
export function ProvidersPeopleSheet() {
  const table = (spec: string, view: PeopleView, rows: { spec: string; person: Person; limits?: number[]; plan?: string }[]) => (
    <ParityTags targets={rows.map((r, i): ParityTarget => ({ spec: r.spec, selector: nthRow(i + 1) }))}>
      <P spec={spec} width={1344}>
        <PeopleTable view={view}>
          {rows.map((r) => (
            <PersonRow key={r.spec} person={r.person} href="#" now={PEOPLE_NOW} activityMax={19} view={view} limits={r.limits} plan={r.plan} />
          ))}
        </PeopleTable>
      </P>
    </ParityTags>
  );
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      <div className="flex items-start gap-2">
        {agentCards.map(({ spec, ...card }) => (
          <P key={spec} spec={spec} width={330}>
            <AgentCard {...card} onClick={fn()} />
          </P>
        ))}
      </div>
      {table("PeopleTable/allAgents", "all", [
        { spec: "PersonRow/mixedCommon", person: peopleRows.mixed },
        { spec: "PersonRow/codexCommon", person: peopleRows.codex },
        { spec: "PersonRow/chatCommon", person: peopleRows.chat },
      ])}
      {table("PeopleTable/codexView", "codex", [
        { spec: "PersonRow/codexView", person: peopleRows.codex, limits: [12, 31], plan: "Plus" },
        { spec: "PersonRow/codexViewLinked", person: peopleViewRows.codexLinked, limits: [41, 83], plan: "Plus" },
      ])}
      {table("PeopleTable/claudeCodeView", "claude-code", [{ spec: "PersonRow/claudeCodeView", person: peopleViewRows.claudeCodeLinked }])}
    </div>
  );
}

/** Issues 2 and 5: the AppBar without agent chips (CompStates markup, §11M 1.3), and ProviderMark. */
export function ProvidersAppBarSheet() {
  const options = (ws: string[]) => ws.map((w) => ({ value: w, label: w }));
  // People keeps `Active in the last` and UpdatedAgo; a person or session page has the live badge and Pause.
  const people = (
    <P spec="AppBar/people" width={1344}>
      <AppBar
        breadcrumb={[{ label: "people" }]}
        window={{ options: options(["24h", "7d", "14d", "All"]), value: "14d", onChange: fn(), "aria-label": "Active in the last" }}
        windowLabel="Active in the last"
        updated={<UpdatedAgo at="2026-11-09T13:57:05Z" now="2026-11-09T13:57:10Z" />}
        onAbout={fn()}
      />
    </P>
  );
  // Every crumb carries the dot(s) of what it points to (§11M 1.4).
  const bar = (spec: string, breadcrumb: Crumb[]) => (
    <P spec={spec} width={1344}>
      <AppBar
        breadcrumb={breadcrumb}
        window={{ options: options(["1h", "24h", "7d", "14d", "All"]), value: "14d", onChange: fn(), "aria-label": "Time window" }}
        live={{ status: "live", intervalS: 5 }}
        onPause={fn()}
        onAbout={fn()}
      />
    </P>
  );
  return (
    <div className="flex flex-col items-start gap-3 p-4">
      {people}
      {bar("AppBar/codexPerson", [{ label: "people", href: "#" }, { label: "Sam Rivera", agents: ["codex"] }])}
      {bar("AppBar/codexSession", [
        { label: "people", href: "#" },
        { label: "Sam Rivera", href: "#", agents: ["codex"] },
        { label: "pomodoro · Nov 9 09:00", agents: ["codex"] },
      ])}
      {bar("AppBar/mixedPersonCodexSession", [
        { label: "people", href: "#" },
        { label: "Chris Ortiz", href: "#", agents: ["claude-code", "codex"] },
        { label: "Add Expo mobile skeleton", agents: ["codex"] },
      ])}
      {bar("AppBar/mixedPerson", [{ label: "people", href: "#" }, { label: "Chris Ortiz", agents: ["claude-code", "codex"] }])}
      <div className="flex flex-wrap items-start gap-3">
        <Cell spec="ProviderMark/claudeCodePerson">
          <ProviderMark agents={["claude-code"]} scope="person" />
        </Cell>
        <Cell spec="ProviderMark/codexPerson">
          <ProviderMark agents={["codex"]} scope="person" />
        </Cell>
        <Cell spec="ProviderMark/mixedPerson">
          <ProviderMark agents={["claude-code", "codex"]} scope="person" linkedBy="email" />
        </Cell>
        <Cell spec="ProviderMark/codexSession">
          <ProviderMark agents={["codex"]} scope="session" client="tui 0.167" />
        </Cell>
      </div>
    </div>
  );
}

/** Issues 3 and 4: the person header by provider, and the Organization stat. */
export function ProvidersPersonSheet() {
  const header = (spec: string, width: number, mark: string, common: string, rows: string[], h: (typeof personHeaders)[keyof typeof personHeaders]) => (
    <ParityTags
      targets={[
        { spec: mark, selector: ".provider-mark" },
        { spec: common, selector: ".person-header-row" },
        ...rows.map((r, i): ParityTarget => ({ spec: r, selector: `.person-header-block > :nth-child(${i + 2})` })),
      ]}
    >
      <P spec={spec} width={width}>
        <PersonHeader {...h} />
      </P>
    </ParityTags>
  );
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      {header("PersonHeader/codexOnly", 1344, "ProviderMark/codexHeader", "PersonHeaderRow/common", ["AgentRow/codex"], personHeaders.codexOnly)}
      {header("PersonHeader/mixed", 1344, "ProviderMark/mixedHeader", "PersonHeaderRow/commonMixed", ["AgentRow/claudeCode", "AgentRow/codexMixed"], personHeaders.mixed)}
      {header("PersonHeader/mixedNarrow", 1024, "ProviderMark/mixedNarrow", "PersonHeaderRow/commonNarrow", ["AgentRow/claudeCodeNarrow", "AgentRow/codexNarrow"], personHeaders.mixed)}
      <div className="flex flex-wrap items-start gap-3">
        <Cell spec="OrgStat/yours">
          <div>
            <OrgStat org={{ kind: "customer", id: "1b9f…e02" }} />
          </div>
        </Cell>
        <Cell spec="OrgStat/outside">
          <div>
            <OrgStat org={{ kind: "outside", id: "5c11…9d0", personal: true }} />
          </div>
        </Cell>
        <Cell spec="OrgStat/unset">
          <div>
            <OrgStat org={{ kind: "unset", id: "1b9f…e02" }} />
          </div>
        </Cell>
      </div>
    </div>
  );
}

/** §11M 2.2, 2.3, 2.7, 3.4: the person's agent scope and the mixed sessions table (common columns only). */
export function ProvidersSessionsSheet() {
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      <Cell spec="AgentScope/person">
        <AgentScope agents={["claude-code", "codex"]} value="all" onChange={fn()} />
      </Cell>
      <ParityTags targets={mixedSessions.map((r, i): ParityTarget => ({ spec: r.spec, selector: nthRow(i + 1) }))}>
        <P spec="SessionTable/mixed" width={1344}>
          <SessionTable view="mixed" expandable>
            {mixedSessions.map((r) => (
              <SessionRow key={r.spec} session={r.session} href="#" now={PEOPLE_NOW} agentColumn={{ client: r.session.agent === "codex" ? r.session.client : null }} nesting={r.nesting} threadFacts={r.threadFacts} />
            ))}
          </SessionTable>
        </P>
      </ParityTags>
    </div>
  );
}

/** §11M 3.3: a linked person's All requests, in All agents (shared chips, agent dots) and scoped to Codex. */
export function ProvidersPersonRequestsSheet() {
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      <P spec="RequestFilters/personAllAgents" width={1344}>
        <RequestFilters channels={PERSON_SHARED_CHANNELS} value={[]} onChange={fn()} hidden={{ count: 41, shown: false, onChange: fn() }} />
      </P>
      <ParityTags targets={personRequests.map((r, i): ParityTarget => ({ spec: r.spec, selector: nthRow(i + 1) }))}>
        <P spec="RequestTable/personAllAgents" width={1344}>
          <RequestTable scope="person" agentDot>
            {personRequests.map((r) => (
              <RequestRow key={r.spec} row={r.row} channel={r.channel} detail={r.detail} showSession agentDot sessionHref={() => "#"} onOpen={fn()} now="2026-11-10T18:00:00" />
            ))}
          </RequestTable>
        </P>
      </ParityTags>
      <Cell spec="AgentScope/personCodex">
        <AgentScope agents={["claude-code", "codex"]} value="codex" onChange={fn()} />
      </Cell>
      <P spec="RequestFilters/personCodex">
        <RequestFilters channels={PERSON_CODEX_CHANNELS} value={[]} onChange={fn()} />
      </P>
    </div>
  );
}

/** The sheet's tooltips (Tooltip/orgUnset, Tooltip/summaryLabel), open and in flow. */
export function ProvidersTooltips() {
  return (
    <div className="flex flex-col items-start gap-3 p-4">
      <div data-parity="Tooltip/orgUnset" className="flex items-start">
        <TooltipSurface content={orgUnsetTip("1b9f…e02")} />
      </div>
      <div data-parity="Tooltip/summaryLabel" className="flex items-start">
        <TooltipSurface content={{ lead: "tool_use · mcp__filesystem__list_allowed_directories ×1" }} />
      </div>
    </div>
  );
}

/** Issues 5, 6 and 7: the session header, tabs, Codex requests and a long turn. */
export function ProvidersSessionSheet() {
  const turnRows = [
    <StepRow key="1" numbered={longTurnSteps.first} />,
    <StepRow key="2" numbered={longTurnSteps.second} />,
    <StepRun key="3" {...longTurnRuns.collapsed} onToggle={fn()} />,
    <StepRow key="42" numbered={longTurnSteps.patch} />,
    <StepRun key="43" {...longTurnRuns.collapsedFailing} onToggle={fn()} />,
  ];
  const noBodyRows = [
    <StepRow key="1" numbered={longTurnStepsNoBodies.first} />,
    <StepRun key="3" {...longTurnRunsNoBodies.collapsed} onToggle={fn()} />,
    <StepRow key="42" numbered={longTurnStepsNoBodies.patch} />,
    <StepRun key="43" {...longTurnRunsNoBodies.run} onToggle={fn()} />,
    <StepRow key="203" numbered={longTurnStepsNoBodies.text} />,
  ];
  // A long turn as the sheet draws it: the prompt row, its strip, the first rows, then `show N more`.
  const longTurn = (spec: string, head: string, item: typeof longTurnPrompt, rows: JSX.Element[], strip: string) => (
    <ParityTags targets={[{ spec: head, selector: ".prompt-row" }]}>
      <div data-parity={spec} className="grid" style={{ gridTemplateColumns: "1344px" }}>
        <div className="dt border border-line bg-surface">
          <div className="dt-grid">
            <PromptRow item={item} expanded onToggle={fn()} now="2026-11-10T10:00:00" />
            <div role="row" className="dt-row step-strip-row">
              <div role="cell" style={{ width: 1240 }}>
                <StepStrip {...LONG_TURN_STRIP} onJump={fn()} />
              </div>
            </div>
            {rows}
            {strip === "StepStrip" && <StepList rows={rows} limit={0} stepsShown={120} totalSteps={203} onMore={fn()} />}
          </div>
        </div>
      </div>
      {strip === "StepStrip/noBodies" && (
        <P spec={strip} width={1240}>
          <StepStrip {...LONG_TURN_STRIP} onJump={fn()} />
        </P>
      )}
    </ParityTags>
  );
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      <ParityTags
        targets={[
          { spec: "ProviderMark/codexSessionHeader", selector: ".provider-mark" },
          { spec: "SessionFacts/codexNoBodies", selector: ".session-facts" },
          { spec: "HeaderStatGroup/session", selector: ".header-stat-group" },
        ]}
      >
        <P spec="SessionHeader/codexMarked" width={1344}>
          {/* An untitled thread without recorded bodies (§11M 3.4, Part 4): cwd, approval, sandbox, origin; no shell or tz. */}
          <SessionHeader
            title="pomodoro · Nov 9 09:00"
            agent="codex"
            client="tui 0.167"
            sub="untitled · thread 01a1…aed1 · 3f2a1c9"
            facts={[
              { k: "cwd", v: "/work/pomodoro" },
              { k: "approval", v: "on-request" },
              { k: "sandbox", v: "workspace-write" },
            ]}
            origin={{ kind: "resume", threadShort: "3b9a…e21", init: "cold_resume", href: "#" }}
            stats={
              <HeaderStatGroup label="Session" wrapGap>
                <HeaderStat label="Span" value="Nov 9 09:00 → 11:04 · 2h 4m" />
                <HeaderStat label="Prompts" value="1" />
                <HeaderStat label="Model calls" value="203" qualifier="+6 prewarm hidden" qualifierTone="hidden" />
                <HeaderStat label="Tokens" value="9.4M → 61k" />
                <HeaderStat label="Reasoning" value="18k" />
                <HeaderStat label="Tool calls" value="168" />
                <HeaderStat label="Device" value="linux · 1 network" />
              </HeaderStatGroup>
            }
          />
        </P>
      </ParityTags>
      <ParityTags targets={[{ spec: "TabHint", selector: ".tab-hint" }]}>
        <P spec="SessionTabs/withHints" width={1344}>
          <SessionTabs
            tabs={sessionTabs}
            value="prompts"
            onChange={fn()}
            hint="What Sam asked, and the work each message started: one prompt, its 203 model calls as steps. Codex calls a prompt a turn."
          />
        </P>
      </ParityTags>
      <P spec="TabHint/requests" width={1344}>
        <TabHint>Every call on the wire for this session, in time order, including calls no prompt started (limits, catalog, metrics).</TabHint>
      </P>
      <P spec="RequestFilters/codex" width={1344}>
        <RequestFilters channels={CODEX_REQUEST_CHANNELS} value={[]} onChange={fn()} hidden={{ count: 6, shown: false, onChange: fn() }} />
      </P>
      <ParityTags targets={codexRequests.map((r, i): ParityTarget => ({ spec: r.spec, selector: nthRow(i + 1) }))}>
        <P spec="RequestTable/codex" width={1344}>
          <RequestTable scope="session">
            {codexRequests.map((r) => (
              <RequestRow key={r.spec} row={r.row} channel={r.channel} detail={r.detail} onOpen={fn()} now="2026-11-09T18:00:00" />
            ))}
          </RequestTable>
        </P>
      </ParityTags>
      <ParityTags targets={codexRequestsNoBodies.map((r, i): ParityTarget => ({ spec: r.spec, selector: nthRow(i + 1) }))}>
        <P spec="RequestTable/codexNoBodies" width={1344}>
          <RequestTable scope="session">
            {codexRequestsNoBodies.map((r) => (
              <RequestRow key={r.spec} row={r.row} channel={r.channel} detail={r.detail} onOpen={fn()} now="2026-11-09T18:00:00" />
            ))}
          </RequestTable>
        </P>
      </ParityTags>
      {longTurn("PromptRow/longTurn", "PromptRow/longTurnHead", longTurnPrompt, turnRows, "StepStrip")}
      {longTurn("PromptRow/longTurnNoBodies", "PromptRow/longTurnHeadNoBodies", longTurnPromptNoBodies, noBodyRows, "StepStrip/noBodies")}
      <P spec="StepStrip" width={1240}>
        <StepStrip {...LONG_TURN_STRIP} onJump={fn()} />
      </P>
      <Row spec="StepRow/longTurnFirst">{turnRows[0]}</Row>
      <Row spec="StepRow/longTurnSecond">{turnRows[1]}</Row>
      <Row spec="StepRun/collapsed">{turnRows[2]}</Row>
      <Row spec="StepRow/longTurnPatch">{turnRows[3]}</Row>
      <Row spec="StepRun/collapsedFailing">{turnRows[4]}</Row>
      <Row spec="StepRow/longTurnNoBodies">{noBodyRows[0]}</Row>
      <Row spec="StepRun/collapsedNoBodies">{noBodyRows[1]}</Row>
      <Row spec="StepRow/longTurnPatchNoBodies">{noBodyRows[2]}</Row>
      <Row spec="StepRun/runNoBodies">{noBodyRows[3]}</Row>
      <Row spec="StepRow/longTurnTextNoBodies">{noBodyRows[4]}</Row>
      <Row spec="StepRun/claudeCodeNoBodies">
        <StepRun {...claudeCodeRunNoBodies} onToggle={fn()} />
      </Row>
      <ParityTags targets={[{ spec: "StepList/more", selector: ".step-list-more" }]}>
        <div className="dt-grid grid items-start" style={{ gridTemplateColumns: "1342px" } as CSSProperties}>
          <StepList rows={turnRows} limit={0} stepsShown={120} totalSteps={203} onMore={fn()} />
        </div>
      </ParityTags>
    </div>
  );
}

/** Issues 8, 9 and 10: headers without dimming, Summary labels, the Codex key strip. */
export function ProvidersDrawerSheet() {
  const strip = (spec: string, tiles: (typeof codexKeyTiles)["call"]) => (
    <ParityTags targets={tiles.map((t, i): ParityTarget => ({ spec: t.spec, selector: `.key-tile:nth-child(${i + 1})` }))}>
      <P spec={spec} width={642}>
        <KeyStrip tiles={tiles} />
      </P>
    </ParityTags>
  );
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      <P spec="HeaderList/noDimming" width={560}>
        <HeaderList headers={codexResponseHeaders} className="border border-line bg-surface" />
      </P>
      {(["fits", "truncated", "long"] as const).map((k) => (
        <P key={k} spec={`SummaryLabel/${k}`} width={520}>
          <SummaryLabel {...summaryLabels[k]} />
        </P>
      ))}
      {strip("KeyStrip/codexCall", codexKeyTiles.call)}
      {strip("KeyStrip/codexNoUsage", codexKeyTiles.noUsage)}
    </div>
  );
}

/** The whole sheet, in the canvas's order. */
export function CompProvidersSheet() {
  return (
    <div className="flex flex-col items-start">
      <ProvidersPeopleSheet />
      <ProvidersAppBarSheet />
      <ProvidersPersonSheet />
      <ProvidersTooltips />
      <ProvidersSessionsSheet />
      <ProvidersSessionSheet />
      <ProvidersPersonRequestsSheet />
      <ProvidersDrawerSheet />
    </div>
  );
}

const meta = {
  title: "Canvas/Providers/ProviderMark",
  parameters: { layout: "fullscreen" },
  excludeStories: ["ProvidersSessionsSheet", "ProvidersPersonRequestsSheet", "ProvidersPeopleSheet", "ProvidersAppBarSheet", "ProvidersPersonSheet", "ProvidersTooltips", "ProvidersSessionSheet", "ProvidersDrawerSheet", "CompProvidersSheet"],
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompProvidersSheet /> };
