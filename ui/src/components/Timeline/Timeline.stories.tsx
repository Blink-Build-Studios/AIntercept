import type { Meta, StoryObj } from "@storybook/react";
import { useState } from "react";
import { BandTrack } from "./BandTrack";
import { BarTrack, gapRowLabel } from "./BarTrack";
import type { Range } from "./Brush";
import sessionCompaction from "../../api/samples/session_timeline/compaction";
import sessionCoverageGap from "../../api/samples/session_timeline/coverage_gap";
import sessionDemo from "../../api/samples/session_timeline/demo";
import sessionMany from "../../api/samples/session_timeline/many";
import sessionOnePrompt from "../../api/samples/session_timeline/one_prompt";
import type { SessionTimeline as SessionTimelineData } from "../../api/types";
import { analyticsBins, bands, callBins, d, friction, gaps, presenceRows, prompts, window } from "./demoData";
import { DensityTrack } from "./DensityTrack";
import { FrictionTrack, frictionRowLabels } from "./FrictionTrack";
import { Lane, LANE_HEIGHT } from "./Lane";
import { MarkerTrack } from "./MarkerTrack";
import { RowTrack } from "./RowTrack";
import { perBin, sessionLanes } from "./sessionLanes";
import { StateTrack } from "./StateTrack";
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
  listNotCaptured: log("list not captured"),
});

function PersonTimeline({ loading }: { loading?: boolean }) {
  const [sel, setSel] = useState<Range | null>(null);
  const on = laneInteractions(d, actions(setSel));
  return (
    <Timeline {...window} label="Activity" selection={sel} onSelect={setSel} loading={loading} brushMode="range" nowTip={d.now ? nowTip(d.now, true) : undefined}>
      <Lane label="Sessions" height={40} summary={`${bands.length} session`} sunken>
        <BandTrack bands={bands} now={d.to} interact={on.bands} />
      </Lane>
      <Lane label="Prompts" height={26} summary={`${prompts.length} prompts`}>
        <MarkerTrack markers={prompts} interact={on.prompts} />
      </Lane>
      <Lane
        label="Model calls"
        sublabel={perBin(d.binSeconds)}
        height={LANE_HEIGHT.barsWithGapRow}
        summary={`peak ${Math.max(...callBins.map((b) => b.v))} per bin; ${gaps.length} gaps`}
        definition={LANE_DEFINITIONS.modelCalls}
        footer={gapRowLabel(gaps)}
      >
        <BarTrack bins={callBins} binSeconds={d.binSeconds} color="model" gaps={gaps} gapRow interact={on.modelCalls} gapTip={on.gapTip} onGap={on.onGap} />
      </Lane>
      <Lane label="Analytics" sublabel="events" height={44} summary={`${analyticsBins.length} active bins`} definition={LANE_DEFINITIONS.analytics}>
        <DensityTrack bins={analyticsBins} binSeconds={d.binSeconds} color="analytics" interact={on.analytics} />
      </Lane>
      <Lane label="Presence" height={LANE_HEIGHT.rows(3)} summary="focused, away and asleep" definition={LANE_DEFINITIONS.presence} rows={presenceRows.map((r) => r.name)}>
        <RowTrack rows={presenceRows} interact={on.presence} />
      </Lane>
      <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary={`${friction.length} marks`} rows={frictionRowLabels(friction)}>
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
      <Lane label="Prompts" height={26} summary={`${l.prompts.markers.length} prompts`}>
        <MarkerTrack markers={l.prompts.markers} labels={l.prompts.labels} interact={on.prompts} />
      </Lane>
      <Lane
        label="Model calls"
        sublabel={l.perBin}
        height={LANE_HEIGHT.barsWithGapRow}
        summary={`${l.modelCalls.bins.length} active bins; ${l.modelCalls.gaps.length} gaps`}
        definition={LANE_DEFINITIONS.modelCalls}
        footer={gapRowLabel(l.modelCalls.gaps)}
      >
        <BarTrack bins={l.modelCalls.bins} binSeconds={l.binSeconds} color="model" gaps={l.modelCalls.gaps} gapRow interact={on.modelCalls} gapTip={on.gapTip} onGap={on.onGap} />
      </Lane>
      <Lane label="Context" sublabel="main thread" height={56} summary={`peak ${l.context.peak} tokens; ${l.context.labels.length} compactions`}>
        <BarTrack bins={l.context.bins} binSeconds={l.binSeconds} color="count" level labels={l.context.labels} interact={own.context} labelTip={own.compactionTip} />
      </Lane>
      <Lane label="Permission" height={30} summary={`${l.permission.segments.length} modes; ${l.permission.changes.length} brief changes`}>
        <StateTrack segments={l.permission.segments} changes={l.permission.changes} palette="permission" nowrap interact={own.permission} />
      </Lane>
      <Lane label="Focus" height={LANE_HEIGHT.rows(2)} summary="this terminal: focused and blurred" rows={l.focusRows.map((r) => r.name)}>
        <RowTrack rows={l.focusRows} interact={on.presence} />
      </Lane>
      <Lane label="Device" height={LANE_HEIGHT.rows(1)} summary="sleeps" rows={l.sleepRows.map((r) => r.name)}>
        <RowTrack rows={l.sleepRows} interact={on.presence} />
      </Lane>
      <Lane label="Network" height={LANE_HEIGHT.single} summary={`${l.network.length} network segments`}>
        <StateTrack segments={l.network} palette="network" nowrap interact={own.network} />
      </Lane>
      <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary={`${l.friction.length} marks`} rows={frictionRowLabels(l.friction)}>
        <FrictionTrack marks={l.friction} interact={on.friction} />
      </Lane>
    </Timeline>
  );
}

const sessionArgs = (data: SessionTimelineData) => ({ from: data.from, to: data.to, label: "Session timeline", children: null });
const sessionStory = (data: SessionTimelineData): Story => ({ args: sessionArgs(data), render: () => <SessionTimeline data={data} /> });

/** The session page from the demo sample (real traffic, sanitized): live, three idle compactions, brief permission flips. */
export const Session: Story = sessionStory(sessionDemo.data);
/** Scenario one_prompt: one prompt, one call. */
export const SessionOnePrompt: Story = sessionStory(sessionOnePrompt.data);
/** Scenario many: three prompts, permission back and forth, a second (IPv6) network, away and asleep. */
export const SessionMany: Story = sessionStory(sessionMany.data);
/** Scenario compaction: an idle compaction (from → to, wait) and one with no result yet. */
export const SessionCompaction: Story = sessionStory(sessionCompaction.data);
/** Scenario coverage_gap: a not-captured window, so the network it overlaps is bypassed (danger). */
export const SessionCoverageGap: Story = sessionStory(sessionCoverageGap.data);
export const SessionLoading: Story = { args: sessionArgs(sessionDemo.data), render: () => <SessionTimeline data={sessionDemo.data} loading /> };
export const Loading: Story = { args: { ...window, label: "Activity", children: null }, render: () => <PersonTimeline loading /> };
/** A Codex thread's lanes: Prompts · Model calls · Approval · Network · Friction (§11J). */
export const Codex: Story = { args: { ...window, label: "Activity", children: null }, render: () => <CodexTimelineSheet /> };
