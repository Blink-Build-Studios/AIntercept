// Parity specimens: CompTurns.dc.html (handoff §11R, §11S) with the sheet's own content, both
// recording modes, measured by `make parity`. Every Canvas/Turns/* story renders this sheet; the
// lanes are on the Timeline sheet (TurnsLaneSpecimens), the work session rows on SessionRow's,
// the header stats on HeaderStat's and the context panel on KeyValueList's.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { CSSProperties, ReactNode } from "react";
import type { Turn } from "../../lib/turns";
import { EMPTY_WORK } from "../../lib/work";
import type { WorkPart } from "../../lib/work";
import { ContentBlock } from "../ContentBlock/ContentBlock";
import { metadataOnly, pasted, reply, toolResult } from "../ContentBlock/specimens";
import { columnVars } from "../DataTable/DataTable";
import { OutcomeSlot } from "../OutcomeSlot/OutcomeSlot";
import { personFacts, personFactsWithContent, sessionFacts, sessionFactsWithContent } from "../OutcomeSlot/specimens";
import { RepoRow, WORK_COLUMNS, WORK_MIN_WIDTH } from "../RepoRow/RepoRow";
import { WorkSessionRow } from "../RepoRow/WorkSessionRow";
import { FrictionTrack, frictionRowLabels, type Friction } from "../Timeline/FrictionTrack";
import { Lane, LANE_HEIGHT } from "../Timeline/Lane";
import { ToolsSpecimen } from "../ToolsOfferedUsed/ToolsOfferedUsed.canvas.stories";
import { Timeline } from "../Timeline/Timeline";
import { TurnTrack } from "../Timeline/TurnTrack";
import { WorkTrack } from "../Timeline/WorkTrack";
import { ParityTags } from "../../test/ParityTag";
import { ReplyPreview } from "./ReplyPreview";
import { SubagentRow } from "./SubagentRow";
import { TURN_COLUMNS, TURN_MIN_WIDTH, TurnRow } from "./TurnRow";
import { TurnStep } from "./TurnStep";
import { WorkGroup } from "./WorkGroup";

/** A specimen inside a 1344px panel: the 1344px row, with the table's hairline under it. */
const P = ({ spec, width = 1344, children }: { spec: string; width?: number; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start dt-grid" style={{ gridTemplateColumns: `${width}px` }}>
    {children}
  </div>
);
/** A panel of several specimens: one table, each row its own specimen. */
const Panel = ({ columns = TURN_COLUMNS, minWidth = TURN_MIN_WIDTH, children }: { columns?: typeof TURN_COLUMNS; minWidth?: number; children: ReactNode }) => (
  <div className="border border-line bg-surface" style={{ width: 1346, ...columnVars(columns), "--dt-min-width": `${minWidth}px` } as CSSProperties}>
    {children}
  </div>
);
const In = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="dt-grid">
    {children}
  </div>
);

type TurnOver = Partial<Turn> & { chars?: number; text?: string | null; images?: number };
const turn = ({ chars = 212, text = null, images = 0, ...over }: TurnOver): Turn => ({
  promptId: "t13",
  n: 13,
  startedAt: "2026-11-03T10:02:00",
  endedAt: null,
  durationMs: 220_000,
  live: false,
  asked: { chars, text, retained: text !== null, pastedBytes: null, images },
  steps: 18,
  tokensIn: 410_000,
  tokensOut: 6_100,
  costUsd: null,
  friction: { frustrated: false, interrupted: false, destructive: 0 },
  work: EMPTY_WORK,
  groups: [],
  subagents: [],
  ...over,
});
const openWork: WorkPart[] = ["read 5 · edited 2 files +31 −9 · ran npm test ×4: ", { failed: "3 failed" }, ", then passed"];
const t14: TurnOver = { promptId: "t14", n: 14, startedAt: "2026-11-03T10:09:00", durationMs: 558_000, chars: 1_400, images: 1, steps: 41, tokensIn: 1_200_000, tokensOut: 14_000 };
const interrupted = { frustrated: false, interrupted: true, destructive: 0 };

export const TURN_SPECIMENS = {
  default: turn({}),
  open: turn(t14),
  interrupted: turn({ promptId: "t15", n: 15, startedAt: "2026-11-03T10:36:00", durationMs: 22_000, chars: 40, steps: 3, tokensIn: 120_000, tokensOut: 400, friction: interrupted }),
  failedCalls: turn({ promptId: "t16", n: 16, startedAt: "2026-11-03T10:41:00", durationMs: 531_000, chars: 388, steps: 37, tokensIn: 1_000_000, tokensOut: 12_000 }),
  live: turn({ promptId: "t34", n: 34, startedAt: "2026-11-04T15:12:00", durationMs: null, live: true, chars: 96, steps: 6, tokensIn: 210_000, tokensOut: 1_100 }),
  withContent: turn({ ...t14, text: "the race is in the token cache — here’s the stack trace…" }),
  codex: turn({ promptId: "c1", n: 1, startedAt: "2026-11-09T09:00:00", durationMs: 7_440_000, chars: 412, steps: 203, tokensIn: 9_400_000, tokensOut: 61_000, friction: interrupted }),
};

// The sheet's Turns and Work lanes: a 1344px frame, 48h, the blocks and bars at the sheet's % positions.
const FROM = Date.parse("2026-11-03T00:00:00Z");
const SPAN = 48 * 3_600_000;
const at = (p: number) => new Date(FROM + Math.round((p / 100) * SPAN)).toISOString();
const blocks = [[2, 3], [6, 6], [14, 9], [25, 0.4], [27, 7], [40, 4], [52, 8], [66, 12], [82, 5], [90, 3]].map(([l = 0, w = 0], i) => ({ id: `b${i}`, index: i + 1, from: at(l), to: at(l + w) }));
const WORK = [[0, 10, 4], [1.6, 22, 8], [3.2, 14, 12], [8, 30, 10], [9.6, 18, 20], [11.2, 8, 2], [12.8, 26, 14], [14.4, 12, 6], [16, 10, 4], [17.6, 22, 8], [19.2, 14, 12], [24, 30, 10], [25.6, 18, 20], [27.2, 8, 2], [28.8, 26, 14], [30.4, 12, 6], [32, 10, 4], [33.6, 22, 8], [35.2, 14, 12], [40, 30, 10], [41.6, 18, 20], [43.2, 8, 2], [44.8, 26, 14], [46.4, 12, 6], [48, 10, 4], [49.6, 22, 8], [51.2, 14, 12], [56, 30, 10], [57.6, 18, 20], [59.2, 8, 2], [60.8, 26, 14], [62.4, 12, 6], [64, 10, 4], [65.6, 22, 8], [67.2, 14, 12], [72, 30, 10], [73.6, 18, 20], [75.2, 8, 2], [76.8, 26, 14], [78.4, 12, 6], [80, 10, 4], [81.6, 22, 8], [83.2, 14, 12], [88, 30, 10], [89.6, 18, 20], [91.2, 8, 2], [92.8, 26, 14], [94.4, 12, 6]];
const workBins = WORK.map(([p = 0, other = 0, edits = 0]) => ({ t: at(p), other, edits }));

/** CompTurns' lanes, for the Timeline sheet (Lane's canvas story) and TurnTrack's and WorkTrack's. */
export function TurnsLaneSpecimens() {
  return (
    <ParityTags targets={[{ spec: "WorkTrack/stacked", selector: "[data-parity='Lane/workSpec'] .tl-work-bars" }]}>
      <div style={{ width: 1344 }}>
        <Timeline from={at(0)} to={at(100)} label="Specimen" gridlines={false}>
          <div data-parity="Lane/turnsSpec" className="contents">
            <Lane label="Turns" height={26} summary="turns">
              <div data-parity="TurnTrack/blocks" className="contents">
                <TurnTrack blocks={blocks} now={at(100)} />
              </div>
            </Lane>
            <span hidden />
          </div>
          <div data-parity="Lane/workSpec" className="contents">
            <Lane label="Work" sublabel="per 15m" height={LANE_HEIGHT.workSession} summary="work per 15 minutes">
              <WorkTrack bins={workBins} binSeconds={(1.6 / 100) * (SPAN / 1000)} maxV={58} />
            </Lane>
            {/* A following sibling keeps the lane's bottom rule, as the sheet draws it. */}
            <span hidden />
          </div>
        </Timeline>
      </div>
      <FrictionSpecimen />
    </ParityTags>
  );
}

// The sheet's friction lane (§11S): ✕ at 41% and 77%, ! twice at 29%, then 52% and 72%. A mark is
// centred on its instant (3.5px left of it); the sheet draws each from its left, so the story's
// instants sit 3.5px later in the 1210px track.
const TRACK_PX = 1344 - 2 - 132;
const glyphAt = (p: number) => at(p + (3.5 / TRACK_PX) * 100);
const fmark = (kind: Friction["kind"], p: number, count = 1): Friction => ({ kind, t: glyphAt(p), count, detail: kind, promptIndex: null, tool: null });
const frictionMarks: Friction[] = [fmark("interrupted", 41), fmark("interrupted", 77), fmark("failed", 29, 2), fmark("failed", 52), fmark("failed", 72)];

function FrictionSpecimen() {
  const row = (n: number) => `[data-parity='Lane/frictionTwoRows'] .tl-track > .tl-row:nth-of-type(${n})`;
  return (
    <ParityTags targets={[{ spec: "FrictionRow/interruptedSpec", selector: row(1) }, { spec: "FrictionRow/failedCalls", selector: row(2) }]}>
      <div style={{ width: 1344 }}>
        <Timeline from={at(0)} to={at(100)} label="Specimen" gridlines={false}>
          <div data-parity="Lane/frictionTwoRows" className="contents">
            <Lane label="Friction" height={LANE_HEIGHT.friction} summary="friction" rows={frictionRowLabels(frictionMarks)} subRows>
              <FrictionTrack marks={frictionMarks} />
            </Lane>
            <span hidden />
          </div>
        </Timeline>
      </div>
    </ParityTags>
  );
}

export function CompTurnsSheet() {
  const toggle = fn();
  return (
    <div className="flex flex-col items-start gap-8" style={{ padding: 48 }}>
      {/* State panels (§11S): tools offered vs used; the context panel is KeyValueList's. */}
      <ToolsSpecimen />

      {/* TurnRow: each alone in a panel, content-free unless named. */}
      <Panel>
        <P spec="TurnRow/default">
          <TurnRow turn={TURN_SPECIMENS.default} work={["Read ×31 · Bash ×6 · Grep ×2"]} dated expanded={false} onToggle={toggle} />
        </P>
      </Panel>
      <Panel>
        <P spec="TurnRow/open">
          <TurnRow turn={TURN_SPECIMENS.open} work={["Read ×5 · Edit ×2 · Bash ×4"]} expanded onToggle={toggle} />
        </P>
      </Panel>
      <Panel>
        <P spec="TurnRow/interruptedTurn">
          <TurnRow turn={TURN_SPECIMENS.interrupted} work={["Edit ×1"]} expanded={false} onToggle={toggle} />
        </P>
      </Panel>
      <Panel>
        <P spec="TurnRow/failedCalls">
          <TurnRow turn={TURN_SPECIMENS.failedCalls} work={["Read ×4 · Edit ×2 · Bash ×3 · 1 subagent"]} failed={1} expanded={false} onToggle={toggle} />
        </P>
      </Panel>
      <Panel>
        <P spec="TurnRow/live">
          <TurnRow turn={TURN_SPECIMENS.live} work={["Read ×3 · Bash ×1 …"]} expanded={false} onToggle={toggle} now="2026-11-04T15:13:04" />
        </P>
      </Panel>
      <Panel>
        <P spec="TurnRow/withContent">
          <TurnRow turn={TURN_SPECIMENS.withContent} work={openWork} expanded={false} onToggle={toggle} />
        </P>
      </Panel>
      <Panel>
        <P spec="TurnRow/codex">
          <TurnRow turn={TURN_SPECIMENS.codex} work={["exec_command ×141 · apply_patch ×18 · mcp:linear ×9"]} dated expanded={false} onToggle={toggle} />
        </P>
      </Panel>

      {/* WorkGroup · TurnStep · SubagentRow: content-free. */}
      <Panel>
        <div className="dt-grid">
          <In spec="WorkGroup/collapsed">
            <WorkGroup kind="read" title="Read ×5" meta="4 steps · 41 s" expanded={false} onToggle={toggle} />
          </In>
          <In spec="WorkGroup/open">
            <WorkGroup kind="run" title="Bash ×4" meta="4 steps · 2m 12s" expanded onToggle={toggle} />
          </In>
          <In spec="TurnStep/default">
            <TurnStep label="step 22" detail="sonnet · 96k → 610 · 4.1 s · tool_use · Bash" onOpen={toggle} />
          </In>
          <In spec="WorkGroup/editsGroup">
            <WorkGroup kind="edit" title="Edit ×2" meta="3 steps" expanded={false} onToggle={toggle} />
          </In>
          <In spec="WorkGroup/mcpGroup">
            <WorkGroup kind="mcp" title="mcp:linear ×3" meta="list_issues ×2 · get_issue · 3 steps" expanded={false} onToggle={toggle} />
          </In>
          <In spec="WorkGroup/thinkingGroup">
            <WorkGroup kind="think" title="No tool" meta="22 steps · 1m 40s · 210k → 3.2k" expanded={false} onToggle={toggle} />
          </In>
          <In spec="WorkGroup/replyGroup">
            <WorkGroup kind="reply" title="Reply" meta="1.2k tokens" expanded={false} onToggle={toggle} />
          </In>
          <In spec="SubagentRow/default">
            <SubagentRow name={null} n={1} work={["9 steps · Write ×1 · Bash ×2"]} tokens="180k → 2.2k" onOpen={toggle} />
          </In>
        </div>
      </Panel>
      {/* With content recorded. */}
      <Panel>
        <div className="dt-grid">
          <In spec="WorkGroup/withPaths">
            <WorkGroup kind="edit" title="Edited 2 files" meta="+31 −9 · src/auth/cache.ts, src/auth/refresh.ts" expanded={false} onToggle={toggle} />
          </In>
          <In spec="WorkGroup/withCommands">
            <WorkGroup kind="run" title="Ran npm test ×4" meta="4 steps · 2m 12s" exits={[1, 1, 1, 0]} expanded onToggle={toggle} />
          </In>
          <In spec="TurnStep/withContent">
            <TurnStep label="step 22" detail="sonnet · 96k → 610 · 4.1 s · Bash npm test · exit 1" onOpen={toggle} />
          </In>
          <In spec="SubagentRow/withContent">
            <SubagentRow name="test-writer" n={1} work={["9 steps · wrote 1 file (.test.ts) +66 · ran npm test ×2: passed"]} tokens="180k → 2.2k" onOpen={toggle} />
          </In>
          <In spec="ReplyPreview">
            <ReplyPreview text="Fixed: two refreshes could race and both write the cache. Now the second waits on the first’s promise…" size="1.2k tokens" onExpand={toggle} />
          </In>
        </div>
      </Panel>

      {/* OutcomeSlot: content-free, then with content. */}
      <div data-parity="OutcomeSlot/session" style={{ width: 1344 }}>
        <OutcomeSlot title="Outcome" facts={sessionFacts} />
      </div>
      <div data-parity="OutcomeSlot/sessionWithContent" style={{ width: 1344 }}>
        <OutcomeSlot title="Outcome" facts={sessionFactsWithContent} />
      </div>
      <div data-parity="OutcomeSlot/personFortnight" style={{ width: 1344 }}>
        <OutcomeSlot title="This fortnight" facts={personFacts} />
      </div>
      <div data-parity="OutcomeSlot/personWithContent" style={{ width: 1344 }}>
        <OutcomeSlot title="This fortnight" facts={personFactsWithContent} />
      </div>

      {/* Activity lanes */}
      <TurnsLaneSpecimens />

      {/* RepoRow · SessionRow (work) */}
      <WorkSpecimens />

      {/* ContentBlock */}
      <div className="flex flex-col" style={{ gap: 10 }}>
        <div data-parity="ContentBlock/pasted" style={{ width: 642 }}>
          <ContentBlock {...pasted} />
        </div>
        <div data-parity="ContentBlock/toolResult" style={{ width: 642 }}>
          <ContentBlock {...toolResult} />
        </div>
        <div data-parity="ContentBlock/reply" style={{ width: 642 }}>
          <ContentBlock {...reply} />
        </div>
        <div data-parity="ContentBlock/metadataOnly" style={{ width: 642 }}>
          <ContentBlock {...metadataOnly} />
        </div>
      </div>
    </div>
  );
}

/** CompTurns' Work tab panels (also on SessionRow's canvas story): content-free flat, then with content by repo. */
export function WorkSpecimens() {
  const now = "2026-11-04T12:00:00";
  return (
    <div className="flex flex-col gap-3">
      <Panel columns={WORK_COLUMNS} minWidth={WORK_MIN_WIDTH}>
        <div className="dt-grid">
          <In spec="SessionRow/workDefault">
            <WorkSessionRow
              flat
              session={{ sessionId: "4c1a8e2077e0", start: "2026-11-02T10:04:00", end: null, live: true, label: "session 4c1a…77e0", turns: 34, activeMs: 22_320_000, friction: { interrupted: 2, failed: 5 } }}
              work={["Bash ×488 · Read ×402 · Edit ×171 · 6 subagents"]}
              href="#"
              now={now}
            />
          </In>
          <In spec="SessionRow/workPlain">
            <WorkSessionRow
              flat
              session={{ sessionId: "91be55c20a4d", start: "2026-10-31T16:20:00", end: "2026-10-31T17:08:00", live: false, label: "session 91be…0a4d", turns: 9, activeMs: 2_880_000, friction: { interrupted: 0, failed: 0 } }}
              work={["Read ×61 · Edit ×12 · mcp:linear ×3"]}
              href="#"
              now={now}
            />
          </In>
        </div>
      </Panel>
      <Panel columns={WORK_COLUMNS} minWidth={WORK_MIN_WIDTH}>
        <div className="dt-grid">
          <In spec="RepoRow/open">
            <RepoRow repo="aintercept" sub="partner-readiness" sessions={1} turns={34} activeMs={22_320_000} work={["edited 23 files · 2 commits · npm test ×41 (9 failed) · PR #2 open"]} expanded onToggle={fn()} />
          </In>
          <In spec="SessionRow/workWithContent">
            <WorkSessionRow
              session={{ sessionId: "4c1a8e2077e0", start: "2026-11-02T10:04:00", end: null, live: true, label: "partner-readiness", turns: 34, activeMs: 22_320_000, friction: { interrupted: 2, failed: 5 } }}
              work={["edited 23 files · 2 commits · npm test ×41 (9 failed)"]}
              href="#"
              now={now}
            />
          </In>
          <In spec="RepoRow/collapsed">
            <RepoRow repo={null} sessions={10} turns={32} activeMs={12_480_000} work={["read 210 files · edited 18 files · used 6 MCP servers"]} expanded={false} onToggle={fn()} />
          </In>
        </div>
      </Panel>
    </div>
  );
}

const meta = { title: "Canvas/Turns/TurnRow", parameters: { layout: "fullscreen" }, excludeStories: ["CompTurnsSheet", "WorkSpecimens", "TurnsLaneSpecimens", "TURN_SPECIMENS"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompTurnsSheet /> };
