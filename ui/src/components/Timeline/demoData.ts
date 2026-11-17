// Track props from the generated person_timeline demo sample (real traffic, sanitized),
// shared by the timeline stories.
import demo from "../../api/samples/person_timeline/demo";
import gapSample from "../../api/samples/person_timeline/coverage_gap";
import type { Band } from "./BandTrack";
import type { TimelineMark } from "./MarkerTrack";

export const d = demo.data;
export const window = { from: d.from, to: d.to, now: d.now };
export const callBins = d.modelCalls.map((b) => ({ t: b.t, v: b.count }));
export const bands: Band[] = d.sessions.map((s) => ({ id: s.sessionId, from: s.start, to: s.end, label: s.label, row: s.row, live: s.live, preCapture: s.capturedFromPrompt !== null }));
export const prompts: TimelineMark[] = d.prompts.map((p) => ({ t: p.t, id: p.promptId, hoverLabel: p.index === null ? undefined : `#${p.index}` }));
export const friction = d.friction;

/** A short window from the coverage_gap scenario (5-minute bins). */
export const small = gapSample.data;
