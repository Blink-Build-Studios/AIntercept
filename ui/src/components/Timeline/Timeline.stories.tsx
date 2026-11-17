import type { Meta, StoryObj } from "@storybook/react";
import { useState } from "react";
import { BandTrack } from "./BandTrack";
import { BarTrack } from "./BarTrack";
import type { Range } from "./Brush";
import sessionDemo from "../../api/samples/session_timeline/demo";
import sessionMany from "../../api/samples/session_timeline/many";
import sessionOnePrompt from "../../api/samples/session_timeline/one_prompt";
import type { SessionTimeline as SessionTimelineData } from "../../api/types";
import { bands, callBins, d, friction, prompts, window } from "./demoData";
import { FrictionTrack, frictionRowLabels } from "./FrictionTrack";
import { Lane, LANE_HEIGHT } from "./Lane";
import { MarkerTrack } from "./MarkerTrack";
import { TurnTrack } from "./TurnTrack";
import { WorkTrack } from "./WorkTrack";
import { perBin, sessionLanes } from "./sessionLanes";
import { Timeline } from "./Timeline";
import { laneInteractions, sessionInteractions, type TimelineActions } from "./interactions";
import { LANE_DEFINITIONS, nowTip } from "./timelineTips";
import { CodexTimelineSheet } from "./CodexTimeline.canvas.stories";

// Click results are logged to the Actions panel; pages wire them to the router and tabs.
const log = (what: string) => (x: unknown) => console.info(what, x);
const actions = (select: (r: Range) => void): TimelineActions => ({
  select,
  openSession: log("open session"),
  openRequest: log("open request"),
  jumpToPrompt: log("jump to prompt"),
});

function PersonTimeline({ loading }: { loading?: boolean }) {
  const [sel, setSel] = useState<Range | null>(null);
  const on = laneInteractions(d, actions(setSel));
  return (
    <Timeline {...window} label="Activity" selection={sel} onSelect={setSel} loading={loading} brushMode="range" nowTip={d.now ? nowTip(d.now, true) : undefined}>
      <Lane label="Sessions" height={44} summary={`${bands.length} session`} sunken>
        <BandTrack bands={bands} now={d.to} interact={on.bands} />
      </Lane>
      <Lane label="Turns" height={24} summary={`${prompts.length} turns`}>
        <MarkerTrack markers={prompts} interact={on.prompts} />
      </Lane>
      <Lane label="Work" sublabel={perBin(d.binSeconds)} height={LANE_HEIGHT.workPerson} summary={`peak ${Math.max(...callBins.map((b) => b.v))} per bin`} definition={LANE_DEFINITIONS.work}>
        <WorkTrack bins={callBins.map((b) => ({ t: b.t, other: b.v, edits: 0 }))} binSeconds={d.binSeconds} interact={on.work} />
      </Lane>
      <Lane label="Friction" height={LANE_HEIGHT.friction} summary={`${friction.length} marks`} rows={frictionRowLabels(friction)} subRows>
        <FrictionTrack marks={friction} interact={on.friction} />
      </Lane>
    </Timeline>
  );
}

const meta = { title: "Components/Timeline/Timeline", component: Timeline, parameters: { layout: "padded" } } satisfies Meta<typeof Timeline>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The person timeline from the demo sample (real traffic, sanitized). Drag to brush; Esc clears. */
export const Person: Story = { args: { ...window, label: "Activity", children: null }, render: () => <PersonTimeline /> };
/**
 * The session page's lanes (handoff §7.3, Session.dc.html) from one
 * `/sessions/{id}/timeline` response, laid out as the page lays them out.
 */
function SessionTimeline({ data, loading }: { data: SessionTimelineData; loading?: boolean }) {
  const [sel, setSel] = useState<Range | null>(null);
  const l = sessionLanes(data);
  const on = laneInteractions(data, actions(setSel));
  const own = sessionInteractions(data, actions(setSel));
  const header = (
    <>
      <span className="tl-key">Session timeline</span>
      <span className="font-mono text-ink-muted">whole session · {l.binsLabel}</span>
      <span className="flex-1" />
      <span className="font-mono text-ink-faint">drag to select a range · everything below follows it</span>
    </>
  );
  return (
    <Timeline {...l.window} label="Session timeline" header={header} selection={sel} onSelect={setSel} loading={loading} brushMode="range" nowTip={data.now ? nowTip(data.now, true) : undefined}>
      <Lane label="Turns" height={26} summary={`${l.turns.length} turns`}>
        <TurnTrack blocks={l.turns} now={data.now ?? data.to} interact={own.turns} />
      </Lane>
      <Lane label="Work" sublabel={l.perBin} height={LANE_HEIGHT.workSession} summary={`${l.work.length} active bins`} definition={LANE_DEFINITIONS.work}>
        <WorkTrack bins={l.work} binSeconds={l.binSeconds} interact={on.work} />
      </Lane>
      <Lane label="Context" sublabel="main thread" height={56} summary={`peak ${l.context.peak} tokens; ${l.context.labels.length} compactions`}>
        <BarTrack bins={l.context.bins} binSeconds={l.binSeconds} color="count" level labels={l.context.labels} interact={own.context} labelTip={own.compactionTip} />
      </Lane>
      <Lane label="Friction" height={LANE_HEIGHT.friction} summary={`${l.friction.length} marks`} rows={frictionRowLabels(l.friction)} subRows>
        <FrictionTrack marks={l.friction} interact={on.friction} />
      </Lane>
    </Timeline>
  );
}

const sessionArgs = (data: SessionTimelineData) => ({ from: data.from, to: data.to, label: "Session timeline", children: null });
const sessionStory = (data: SessionTimelineData): Story => ({ args: sessionArgs(data), render: () => <SessionTimeline data={data} /> });

/** The session page from the demo sample (real traffic, sanitized): live. */
export const Session: Story = sessionStory(sessionDemo.data);
/** Scenario one_prompt: one prompt, one call. */
export const SessionOnePrompt: Story = sessionStory(sessionOnePrompt.data);
/** Scenario many: three prompts. */
export const SessionMany: Story = sessionStory(sessionMany.data);
export const SessionLoading: Story = { args: sessionArgs(sessionDemo.data), render: () => <SessionTimeline data={sessionDemo.data} loading /> };
export const Loading: Story = { args: { ...window, label: "Activity", children: null }, render: () => <PersonTimeline loading /> };
/** A Codex thread's lanes: Prompts · Model calls · Friction (§11J). */
export const Codex: Story = { args: { ...window, label: "Activity", children: null }, render: () => <CodexTimelineSheet /> };
