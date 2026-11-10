// Track props from the generated person_timeline demo sample (real traffic, sanitized),
// shared by the timeline stories.
import demo from "../../api/samples/person_timeline/demo";
import gapSample from "../../api/samples/person_timeline/coverage_gap";
import type { Band } from "./BandTrack";
import type { TimelineMark } from "./MarkerTrack";
import type { StateRow } from "./RowTrack";
import type { Segment } from "./StateTrack";

export const d = demo.data;
export const window = { from: d.from, to: d.to, now: d.now };
export const callBins = d.modelCalls.map((b) => ({ t: b.t, v: b.count }));
export const analyticsBins = d.analytics.map((b) => ({ t: b.t, v: b.count }));
export const gaps = d.notCaptured.map((g) => ({ from: g.from, to: g.to, count: g.count }));
export const bands: Band[] = d.sessions.map((s) => ({ id: s.sessionId, from: s.start, to: s.end, label: s.label, row: s.row, live: s.live, preCapture: s.capturedFromPrompt !== null, capturedFrom: s.capturedFromPrompt }));
export const presence: Segment[] = d.presence.map((p) => ({ from: p.from, to: p.to, state: p.state, label: p.label }));
export const presenceRows: StateRow[] = (
  [
    ["focused", "focused", "ink"],
    ["away", "away", "muted"],
    ["asleep", "device asleep", "faint"],
  ] as const
).map(([state, name, tone]) => ({ name, tone, segments: presence.filter((p) => p.state === state) }));
export const prompts: TimelineMark[] = d.prompts.map((p) => ({ t: p.t, id: p.promptId, hoverLabel: p.index === null ? undefined : `#${p.index}` }));
export const friction = d.friction;

/** A short window from the coverage_gap scenario (5-minute bins). */
export const small = gapSample.data;
