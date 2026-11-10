// Parity specimens from CompAgents.dc.html (handoff §11J, round 13: Codex and the
// provider-neutral UI), measured against the canvas by `make parity`. Every component
// with a specimen there renders this sheet (or its part) in its own Canvas story.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { CSSProperties, ReactNode } from "react";
import { ParityTags } from "../../test/ParityTag";
import { AgentPanel } from "../AgentPanel/AgentPanel";
import { AgentStatGroup } from "../AgentStatGroup/AgentStatGroup";
import { columnVars, type Column } from "../DataTable/DataTable";
import { HeaderStat } from "../HeaderStat/HeaderStat";
import { HeaderStatGroup } from "../HeaderStatGroup/HeaderStatGroup";
import { HiddenCallsNote } from "../HiddenCallsNote/HiddenCallsNote";
import { PersonIdentity } from "../PersonIdentity/PersonIdentity";
import { PERSON_COLUMNS, PERSON_MIN_WIDTH, PersonRow } from "../PersonRow/PersonRow";
import { PromptRow } from "../PromptRow/PromptRow";
import { DetailSection } from "../RequestDrawer/DetailSection";
import { SessionHeader } from "../SessionHeader/SessionHeader";
import { SESSION_AGENT_COLUMNS, SESSION_AGENT_MIN_WIDTH, SessionRow } from "../SessionRow/SessionRow";
import { StepRow, ToolCallRow } from "../StepRow/StepRow";
import { TurnReport } from "../TurnReport/TurnReport";
import { Value } from "../Value/Value";
import {
  CODEX_LIMITS,
  NOW,
  codexExec,
  codexFacts,
  codexLimitsMore,
  codexLimitsTip,
  codexModelCall,
  codexPanelRows,
  codexPatch,
  codexPatchChange,
  codexPerson,
  codexPrompt,
  codexResponseRows,
  codexSpawn,
  forkOrigin,
  linkedAccounts,
  linkedPerson,
  person,
  sessions,
  turnReportParts,
} from "./agentSpecimens";

/** A specimen in a fixed-width column (the sheet's staging width), or at its own size. */
const P = ({ spec, width, children }: { spec: string; width?: number; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start justify-items-start" style={width ? ({ gridTemplateColumns: `${width}px`, justifyItems: "stretch" } as CSSProperties) : undefined}>
    {children}
  </div>
);
/** The sheet's `.cell`: a flex column, so an inline value is a block of its own width. */
const Cell = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="flex flex-col items-start">
    {children}
  </div>
);

/** Screens 1 and 2: n/a against unknown, and the places provider extras live. */
export function AgentPlacesSheet() {
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      <div className="flex gap-3">
        <Cell spec="Value/unknown">
          <Value unknown="not extracted yet" />
        </Cell>
        {/* Value/notApplicable and NotReportedLine are retired (§11L: no placeholders). */}
      </div>
      <ParityTags targets={[{ spec: "AgentStatGroup/codexLimits", selector: ".agent-stat-group" }]}>
        <P spec="HeaderStatGroup/withAgentExtras" width={1344}>
          <HeaderStatGroup>
            <HeaderStat label="Plan" value="ChatGPT Plus" />
            <HeaderStat label="Devices" value="2" />
            <HeaderStat label="Last seen" value="now" live />
            <AgentStatGroup agent="codex" topic="limits" value={CODEX_LIMITS} tip={codexLimitsTip} more={codexLimitsMore} />
          </HeaderStatGroup>
        </P>
      </ParityTags>
      <div className="flex items-start gap-6">
        <P spec="AgentPanel/codexSession" width={402}>
          <AgentPanel agent="codex" topic="this thread" rows={codexPanelRows} />
        </P>
        <P spec="DetailSection/codexResponse" width={560}>
          <DetailSection agent="codex" title="response" className="border border-line bg-surface" rows={codexResponseRows} />
        </P>
      </div>
      <P spec="PersonIdentity/linked" width={820}>
        <PersonIdentity name="Chris Ortiz" email="chris@example.com" linkedBy="email" accounts={linkedAccounts} />
      </P>
    </div>
  );
}

/** Gaps 1, 2, 4, 5, 6, 7: the session header, the turn report, hidden calls and limits. */
export function AgentSessionSheet() {
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      <ParityTags
        targets={[
          { spec: "SessionFacts", selector: ".session-facts" },
          { spec: "ThreadOrigin", selector: ".thread-origin" },
        ]}
      >
        <P spec="SessionHeader/codex" width={1344}>
          <SessionHeader
            title="Add Expo mobile skeleton"
            agent="codex"
            client="tui 0.167"
            sub="pomodoro · 9be04d1 · thread 7c1e…a90"
            facts={codexFacts}
            origin={forkOrigin}
            stats={
              <HeaderStatGroup>
                <HeaderStat label="Prompts" value="9" />
                <HeaderStat label="Model calls" value="63" qualifier="+6 prewarm hidden" qualifierTone="hidden" />
                <HeaderStat label="Tokens" value="412k → 9.8k" />
                <HeaderStat label="Reasoning" value="3.1k" />
                <HeaderStat label="Tool calls" value="58" />
                <HeaderStat label="Span" value="42m 5s" />
              </HeaderStatGroup>
            }
          />
        </P>
      </ParityTags>
      <P spec="TurnReport" width={1342}>
        <TurnReport agent="codex" parts={turnReportParts} />
      </P>
      <P spec="HiddenCallsNote" width={1342}>
        <HiddenCallsNote counts={{ prewarm: 1 }} onShow={fn()} />
      </P>
      <div data-parity="AgentStatGroup/claudeCodeLimits">
        <AgentStatGroup agent="claude-code" topic="limits" value="7d 83% · overage off" />
      </div>
    </div>
  );
}

// Screens 3 and 5, gaps 3 and 6: rows in the sheet's `.frame` (1344px inside a 1px
// border), each with the table's hairline under it.
const FRAME = 1342;
const Row = ({ spec, columns, minWidth, children }: { spec: string; columns?: Column[]; minWidth?: number; children: ReactNode }) => (
  <div
    data-parity={spec}
    className="dt-grid grid items-start"
    style={{ gridTemplateColumns: `${FRAME}px`, ...(columns ? columnVars(columns) : {}), ...(minWidth ? { "--dt-min-width": `${minWidth}px` } : {}) } as CSSProperties}
  >
    {children}
  </div>
);
const PersonR = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <Row spec={spec} columns={PERSON_COLUMNS} minWidth={PERSON_MIN_WIDTH}>
    {children}
  </Row>
);
const SessionR = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <Row spec={spec} columns={SESSION_AGENT_COLUMNS} minWidth={SESSION_AGENT_MIN_WIDTH}>
    {children}
  </Row>
);

/** Mixed lists, the linked person, nested threads; a Codex turn's rows. */
export function AgentRowsSheet() {
  return (
    <div className="flex flex-col items-start gap-3 p-4">
      <PersonR spec="PersonRow/linked">
        <PersonRow person={linkedPerson} href="#" now={NOW} view="claude-code" idLine="linked by email · 2 accounts · 3 devices" />
      </PersonR>
      <PersonR spec="PersonRow/codexOnly">
        <PersonRow person={codexPerson} href="#" now={NOW} view="codex" plan="Plus" />
      </PersonR>
      <PersonR spec="PersonRow/claudeCodeOnly">
        <PersonRow person={person} href="#" now={NOW} view="claude-code" />
      </PersonR>
      <SessionR spec="SessionRow/claudeCode">
        <SessionRow session={sessions.claudeCode} href="#" now={NOW} agentColumn={{}} />
      </SessionR>
      <SessionR spec="SessionRow/codexParent">
        <SessionRow session={sessions.codexParent} href="#" now={NOW} agentColumn={{ client: "exec" }} nesting={{ kind: "parent", expanded: true, onToggle: fn() }} threadFacts="· 3 subagent · 1 review" />
      </SessionR>
      <SessionR spec="SessionRow/codexChild">
        <SessionRow session={sessions.codexChild} href="#" now={NOW} agentColumn={{ client: "exec" }} nesting={{ kind: "child", agentName: "timer_edge_tests" }} />
      </SessionR>
      <SessionR spec="SessionRow/codexReview">
        <SessionRow session={sessions.codexReview} href="#" now={NOW} agentColumn={{ client: "exec" }} nesting={{ kind: "child", agentName: "review_slugify" }} />
      </SessionR>
      <SessionR spec="SessionRow/codexFork">
        <SessionRow
          session={sessions.codexFork}
          href="#"
          now={NOW}
          agentColumn={{ client: "tui" }}
          nesting={{ kind: "parent", expanded: false, onToggle: fn() }}
          threadFacts="· fork of 3b9a… at #4"
          absent={{ missed: true }}
        />
      </SessionR>
      <Row spec="PromptRow/codexExpanded">
        <PromptRow item={codexPrompt} expanded onToggle={fn()} now={NOW} />
      </Row>
      <Row spec="StepRow/codexModelCall">
        <StepRow step={codexModelCall} extras={{ reasoning: 212, effort: "medium" }} onOpen={fn()} />
      </Row>
      <Row spec="ToolCallRow/codexExec">
        <ToolCallRow tool={codexExec} extras={{ approval: "approved (on-request)" }} onOpen={fn()} />
      </Row>
      <Row spec="ToolCallRow/codexPatch">
        <ToolCallRow tool={codexPatch} extras={{ patch: codexPatchChange }} onOpen={fn()} />
      </Row>
      <Row spec="StepRow/codexSubagent">
        <StepRow spawn={codexSpawn} />
      </Row>
    </div>
  );
}

/** The whole sheet, in the canvas's order. */
export function CompAgentsSheet() {
  return (
    <div className="flex flex-col items-start">
      <AgentPlacesSheet />
      <AgentRowsSheet />
      <AgentSessionSheet />
    </div>
  );
}

const meta = {
  title: "Canvas/Agents/AgentSectionHead",
  parameters: { layout: "fullscreen" },
  excludeStories: ["AgentPlacesSheet", "AgentRowsSheet", "AgentSessionSheet", "CompAgentsSheet"],
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompAgentsSheet /> };
