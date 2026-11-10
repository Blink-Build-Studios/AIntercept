// Parity specimens for timeline hover (CompTimelineHover.dc.html, handoff §6B.4):
// every Lane/BarTrack/BandTrack/StateTrack/MarkerTrack/TimeAxis/Brush
// hover, focus and pressed specimen, drawn with our components in forced
// states and the sheet's own data (bar heights in px against the lane's
// height; positions in % of the window). The sheet's tooltips are measured on
// their own (Tooltip.canvas.stories.tsx); the ones it draws inside a frame are
// drawn here too, so the frame's subtree matches.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { createLinkedHoverStore, LinkedHoverProvider } from "../../lib/linkedHover";
import { ParityTags, type ParityTarget } from "../../test/ParityTag";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { BandTrack, type Band } from "./BandTrack";
import { BarTrack, type Bin } from "./BarTrack";
import { DensityTrack } from "./DensityTrack";
import type { MarkForce } from "./TimelineGuide";
import { Lane } from "./Lane";
import { RowTrack, type StateRow } from "./RowTrack";
import { LANE_HEIGHT } from "./Lane";
import { MarkerTrack, type TimelineMark } from "./MarkerTrack";
import { StateTrack } from "./StateTrack";
import { Timeline } from "./Timeline";

// The sheet's axis reads Nov 2 08:00 → Nov 4 00:00: 40h, in the viewer's zone.
const FROM = new Date(2026, 10, 2, 8).getTime();
const SPAN = 40 * 3_600_000;
const iso = (t: number) => new Date(t).toISOString();
const at = (p: number) => iso(FROM + Math.round((p / 100) * SPAN));
const W = { from: iso(FROM), to: iso(FROM + SPAN) };
/** Seconds per bin when a bin is `pct`% of the window. */
const binOf = (pct: number) => (SPAN * pct) / 100 / 1000;

// Model calls: 60 bins (1.67%), bars 80% wide, heights in px of the 60px lane.
const MODEL_BIN = binOf(100 / 60);
const modelHeights: [number, number][] = [
  [5, 15], [6, 30], [7, 21], [8, 32], [9, 34], [10, 7], [11, 4], [12, 44], [13, 16], [14, 15], [15, 51], [16, 26], [17, 44],
  [22, 26], [23, 34], [24, 11], [25, 34], [26, 45], [27, 29], [28, 39], [29, 36], [30, 7], [31, 40],
  [37, 32], [38, 18], [39, 5], [40, 45], [41, 26], [42, 38], [43, 46], [44, 38], [45, 48], [46, 22], [47, 42], [48, 25], [49, 48], [50, 46], [51, 8], [52, 10], [53, 14], [54, 50], [55, 24], [56, 34], [57, 18],
];
const modelBins: Bin[] = modelHeights.map(([i, v]) => ({ t: iso(FROM + i * MODEL_BIN * 1000), v }));
/** The hovered bin: #28, Nov 3 10:00–10:30 on the sheet's tooltip. */
const HOVER_BIN = iso(FROM + 28 * MODEL_BIN * 1000 + 1000);
/** The guide snaps to the bin's centre. */
const HOVER_CENTRE = FROM + 28.5 * MODEL_BIN * 1000;
// Analytics: 100 bins (1%), heights in px of the 44px lane.
const ANALYTICS_BIN = binOf(1);
const analyticsHeights = [
  2, 2, 2, 2, 2, 2, 2, 2, 2, 16, 12, 20, 14, 11, 20, 33, 30, 29, 15, 23, 17, 14, 12, 15, 34, 31, 30, 30, 15, 18, 26, 29, 32, 32, 12, 25, 27, 23, 14, 22, 12, 34, 32, 24, 17, 33, 24, 32,
  32, 23, 20, 25, 21, 14, 17, 31, 11, 11, 26, 17, 23, 22, 18, 35, 15, 20, 15, 26, 17, 19, 29, 18, 24, 33, 12, 11, 15, 29, 26, 16, 18, 14, 21, 11, 28, 33, 34, 29, 34, 10, 17, 35, 30, 20,
  34, 26, 2, 2, 2, 2,
];
const analyticsBins: Bin[] = analyticsHeights.map((v, i) => ({ t: iso(FROM + i * ANALYTICS_BIN * 1000), v }));
// Context (session page, drawn as on Session): slots of 100/100.8% from slot 4 to 100, heights in px under the 4px headroom.
const CONTEXT_SLOT = 100 / 100.8;
const CONTEXT_BIN = binOf(CONTEXT_SLOT);
const contextHeights = [
  3, 5, 7, 9, 11, 12, 14, 15, 17, 20, 22, 23, 26, 27, 29, 31, 33, 34, 36, 38, 39, 41, 44, 45, 47, 49, 50, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52, 52,
  52, 52, 52, 52, 52, 1, 3, 4, 7, 8, 10, 12, 13, 15, 17, 19, 21, 23, 24, 26, 28, 30, 32, 33, 35, 37, 39, 41, 2, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 5, 7, 9, 10, 12,
];
const slotAt = (i: number) => iso(FROM + Math.round((i * CONTEXT_SLOT * SPAN) / 100));
const contextBins: Bin[] = contextHeights.map((v, i) => ({ t: slotAt(i + 4), v }));
/** The hovered context bar: slot 68. */
const CONTEXT_HOVER = iso(Date.parse(slotAt(68)) + 1000);
const compactions = [
  { t: at(54.167), text: "⇣ compaction 905k → 21k · 1m 11s" },
  { t: at(77.381), text: "⇣ idle compaction 904k" },
];

const ticksGuide = [10.3, 13, 17, 20, 24, 27, 38, 41, 45, 49, 60.3, 63, 66, 70, 74, 79, 84, 88, 91, 94];
const ticksHover = [10.3, 13, 17, 20, 24, 27, 38, 41, 49, 60.3, 63, 66];

const tip = (c: TooltipContent) => () => c;
const none = () => null;
const label = () => "bin";

const barBin: TooltipContent = {
  title: "Nov 3 10:00–10:30",
  rows: [
    { key: "Model calls", value: "38" },
    { key: "", value: "main 31 · subagent 5 · auxiliary 2" },
    { key: "Tokens", value: "1.2M in → 41k out" },
    { key: "Not captured", value: "0" },
  ],
  hint: "Click to select this half hour · drag to select a range",
};

/** The canvas `.frame`: 1100px inside a 1px border, with the sheet's top padding for its tooltip. */
const Frame = ({ width = 1102, sheet, children }: { width?: number; sheet?: string; children: ReactNode }) => (
  <div data-sheet={sheet} style={{ width }}>
    {children}
  </div>
);
/** A specimen inside a lane or timeline: no box of its own. */
const In = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="contents">
    {children}
  </div>
);
/** A lane alone in a frame, as the sheet draws most hover specimens. */
const One = ({ width, sheet, children }: { width?: number; sheet?: string; children: ReactNode }) => (
  <Frame width={width} sheet={sheet}>
    <Timeline {...W} label="Specimen" gridlines={false}>
      {children}
    </Timeline>
  </Frame>
);
const forceAt = (t: string, state: MarkForce) => ({ t, state });

// The person page's presence rows (§11G.A2), as on the hover sheet.
const presence: StateRow[] = [
  { name: "focused", tone: "ink", segments: [{ from: at(4), to: at(34), state: "focused" }, { from: at(48), to: at(70), state: "focused" }] },
  { name: "away", tone: "muted", segments: [{ from: at(34), to: at(48), state: "away" }] },
  { name: "device asleep", tone: "faint", segments: [{ from: at(72), to: at(84), state: "asleep" }] },
];
// The person page's sessions lane, as on Main: one row of bands.
const bands: Band[] = [
  { id: "a", from: at(10.2), to: at(11.9), label: "", row: 0, live: false },
  { id: "b", from: at(12.2), to: at(13.2), label: "", row: 0, live: false },
  { id: "linked", from: at(24), to: at(54), label: "282 calls", row: 0, live: false },
  { id: "d", from: at(70), to: at(98), label: "aintercept · partner-readiness · live", row: 0, live: false },
];

// Specimens inside our components, found by selector (ParityTags).
const targets: ParityTarget[] = [
  { spec: "TimeAxis/withReadout", selector: "[data-sheet=guide] .tl-axis" },
  { spec: "TimeAxis/hoverReadout", selector: "[data-sheet=guide] .tl-readout" },
  { spec: "BandTrack/hoverBand", selector: "[data-sheet=bands] .tl-band[data-force]" },
  { spec: "StateTrack/hoverSegment", selector: "[data-sheet=presence] .tl-rowbar[data-force]" },
  { spec: "StateTrack/hoverTick", selector: "[data-sheet=permission] .tl-change[data-force]" },
  { spec: "StateTrack/hoverNetwork", selector: "[data-sheet=network] .tl-state[data-force]" },
  { spec: "MarkerTrack/hoverTick", selector: "[data-sheet=prompts] .tl-hit[data-force] > .tl-tick" },
  { spec: "MarkerTrack/hoverGlyph", selector: "[data-sheet=friction] .tl-glyph[data-force]" },
  { spec: "BarTrack/contextHoverBar", selector: "[data-sheet=context] .tl-bar-hover" },
  { spec: "ContextTrack/compactionLabelHover", selector: "[data-sheet=contextLabel] .tl-compaction[data-force]" },
  { spec: "Brush/hoverHandleSelection", selector: "[data-sheet=handle] .tl-sel" },
  { spec: "Brush/handleHover", selector: "[data-sheet=handle] .tl-handle-halo" },
  { spec: "Brush/dragging", selector: "[data-sheet=dragging] .tl-sel" },
  { spec: "BandTrack/focusBand", selector: "[data-sheet=focusBand] .tl-band[data-force]" },
  { spec: "BandTrack/linkedBand", selector: "[data-sheet=linkedBand] .tl-band[data-force=linked]" },
  { spec: "MarkerTrack/linkedTick", selector: "[data-sheet=linkedTick] .tl-hit[data-force=linked] > .tl-tick" },
];

// CompTableHover: a row pointed at in a table (§6B.5).
const rowOnBand = createLinkedHoverStore();
rowOnBand.set({ source: "table", ids: ["linked"], span: [at(24), at(54)] });
const rowOnTick = createLinkedHoverStore();
rowOnTick.set({ source: "table", ids: ["p19"], t: at(45) });

export function TimelineHoverSheet() {
  return (
    <ParityTags targets={targets}>
    <div className="flex flex-col items-start gap-8 p-4" style={{ paddingTop: 220 } as CSSProperties}>
      {/* Guide + bin hover: the guide through every lane, the axis readout, the bin's tooltip above the timeline. */}
      <Frame sheet="guide">
        <In spec="Timeline/hoverGuide">
          <Timeline {...W} label="Specimen" gridlines={false} guide={{ t: HOVER_CENTRE, readout: "Nov 3 10:15" }}>
            {/* Lanes chained so each but the last has a following sibling (its bottom rule). */}
            <In spec="Lane/promptsHoverGuide">
              <Lane label="Prompts" height={26} summary="prompts">
                <MarkerTrack markers={ticksGuide.map((p): TimelineMark => ({ t: at(p) }))} />
              </Lane>
              <In spec="Lane/modelCallsHover">
                <Lane label="Model calls" sublabel="per 30 min" height={60} summary="model calls">
                  <In spec="BarTrack/hoverBin">
                    <BarTrack bins={modelBins} binSeconds={MODEL_BIN} color="model" maxV={60} interact={{ tip: tip(barBin), label, force: forceAt(HOVER_BIN, "hover") }} />
                  </In>
                </Lane>
                <In spec="Lane/analyticsHoverGuide">
                  <Lane label="Analytics" sublabel="events" height={44} summary="analytics">
                    <DensityTrack bins={analyticsBins} binSeconds={ANALYTICS_BIN} color="analytics" maxV={44} />
                  </Lane>
                  <In spec="Lane/presenceHoverGuide">
                    <Lane label="Presence" height={LANE_HEIGHT.rows(3)} summary="presence" rows={presence.map((r) => r.name)}>
                      <RowTrack rows={presence} />
                    </Lane>
                  </In>
                </In>
              </In>
            </In>
          </Timeline>
        </In>
      </Frame>

      {/* Not captured: the window outlined in danger. */}
      <One>
        <In spec="Lane/notCapturedHover">
          <Lane label="Model calls" sublabel="per 30 min" height={60} summary="model calls">
            <In spec="BarTrack/hoverGap">
              <BarTrack
                bins={modelBins}
                binSeconds={MODEL_BIN}
                color="model"
                maxV={60}
                gaps={[{ from: at(52), to: at(60 - 100 / 60), count: 57 }]}
                gapTip={() => ({ body: "" })}
                forceGap={forceAt(at(53), "hover")}
              />
            </In>
          </Lane>
        </In>
      </One>

      {/* Analytics bin. */}
      <One>
        <In spec="Lane/analyticsHover">
          <Lane label="Analytics" sublabel="events" height={44} summary="analytics">
            <In spec="DensityTrack/hoverBin">
              <DensityTrack bins={analyticsBins} binSeconds={ANALYTICS_BIN} color="analytics" maxV={44} interact={{ tip: none, label, force: forceAt(at(47.5), "hover") }} />
            </In>
          </Lane>
        </In>
      </One>

      {/* Session band (person page). */}
      <One sheet="bands">
        <In spec="Lane/sessionsHover">
          <Lane label="Sessions" height={44} summary="sessions" sunken>
            <BandTrack bands={bands} now={W.to} interact={{ tip: () => ({ body: "" }), force: { id: "linked", state: "hover" } }} />
          </Lane>
        </In>
      </One>

      {/* Context bar (session page): the hovered bar in ink over a link-fill column. */}
      <One sheet="context">
        <In spec="Lane/contextHover">
          <Lane label="Context" sublabel="tokens · main thread" height={56} summary="context">
            <In spec="BarTrack/contextHover">
              <BarTrack
                bins={contextBins}
                binSeconds={CONTEXT_BIN}
                color="count"
                level
                maxV={52}
                interact={{ tip: none, label, force: forceAt(CONTEXT_HOVER, "hover") }}
                labels={compactions}
              />
            </In>
          </Lane>
        </In>
      </One>
      {/* A compaction label hovered: ink with a dotted muted underline. */}
      <One sheet="contextLabel">
        <In spec="Lane/contextLabelHover">
          <Lane label="Context" sublabel="tokens · main thread" height={56} summary="context">
            <In spec="BarTrack/contextLabelHover">
              <BarTrack
                bins={contextBins}
                binSeconds={CONTEXT_BIN}
                color="count"
                level
                maxV={52}
                interact={{ tip: none, label }}
                labels={compactions}
                labelTip={() => ({ body: "" })}
                forceLabel={forceAt(at(54.167), "hover")}
              />
            </In>
          </Lane>
        </In>
      </One>

      {/* State segments. */}
      <One sheet="presence">
        <In spec="Lane/presenceHover">
          <Lane label="Presence" height={LANE_HEIGHT.rows(3)} summary="presence" rows={presence.map((r) => r.name)}>
            <RowTrack rows={presence} interact={{ tip: none, force: { at: at(34), state: "hover" } }} />
          </Lane>
        </In>
      </One>
      <One sheet="permission">
        <In spec="Lane/permissionHover">
          <Lane label="Permission" height={30} summary="permission">
            <StateTrack
              palette="permission"
              segments={[{ from: at(4), to: at(96), state: "bypassPermissions" }]}
              changes={[{ t: at(58), label: "→ default ×2 in 20 s" }]}
              interact={{ tip: none, changeTip: () => ({ body: "" }), force: { at: at(58), state: "hover" } }}
            />
          </Lane>
        </In>
      </One>
      <One sheet="network">
        <In spec="Lane/networkHover">
          <Lane label="Network" height={30} summary="network">
            <StateTrack
              palette="network"
              segments={[
                { from: at(4), to: at(34), state: "ok", text: "net A · home" },
                { from: at(34), to: at(74), state: "bypassed", text: "net B · has IPv6 → bypassed proxy" },
              ]}
              interact={{ tip: none, force: { at: at(34), state: "hover" } }}
            />
          </Lane>
        </In>
      </One>

      {/* Markers. */}
      <One sheet="prompts">
        <In spec="Lane/promptsHover">
          <Lane label="Prompts" height={26} summary="prompts">
            <MarkerTrack
              markers={[...ticksHover.map((p): TimelineMark => ({ t: at(p) })), { t: at(45), hoverLabel: "#19" }]}
              interact={{ tip: none, force: { t: at(45), state: "hover" } }}
            />
          </Lane>
        </In>
      </One>
      <One sheet="friction">
        <In spec="Lane/frictionHover">
          <Lane label="Friction" height={26} summary="friction">
            <MarkerTrack
              markers={[
                { t: at(14), glyph: "↯" },
                { t: at(30), glyph: "✕" },
                { t: at(47.6), glyph: "▲", tone: "danger" },
              ]}
              interact={{ tip: none, force: { t: at(47.6), state: "hover" } }}
            />
          </Lane>
        </In>
      </One>

      {/* Brush over the model calls lane: the start handle hovered; a drag on the end edge (its handle grown). */}
      <Frame sheet="handle">
        <Timeline {...W} label="Specimen" gridlines={false} selection={{ from: at(30), to: at(45) }} onSelect={() => {}} brushMode="range" brushForce="handleHover">
          <In spec="Lane/brushhoverHandleSelection">
            <Lane label="Model calls" height={60} summary="model calls">
              <BarTrack bins={modelBins} binSeconds={MODEL_BIN} color="model" maxV={60} />
            </Lane>
          </In>
        </Timeline>
      </Frame>
      <Frame sheet="dragging">
        <Timeline {...W} label="Specimen" gridlines={false} selection={{ from: at(30), to: at(52) }} onSelect={() => {}} brushMode="range" brushForce="dragging">
          <In spec="Lane/brushdragging">
            <Lane label="Model calls" height={60} summary="model calls">
              <BarTrack bins={modelBins} binSeconds={MODEL_BIN} color="model" maxV={60} />
            </Lane>
          </In>
        </Timeline>
      </Frame>

      {/* Linked from a table row: the band in hover style with the span fill; the prompt tick. */}
      <LinkedHoverProvider store={rowOnBand}>
        <One sheet="linkedBand" width={1346}>
          <Lane label="Sessions" height={44} summary="sessions" sunken>
            <BandTrack bands={bands} now={W.to} interact={{ tip: () => ({ body: "" }) }} />
          </Lane>
        </One>
      </LinkedHoverProvider>
      <LinkedHoverProvider store={rowOnTick}>
        <One sheet="linkedTick">
          <Lane label="Prompts" height={26} summary="prompts">
            <MarkerTrack markers={[{ t: at(41) }, { t: at(45), id: "p19", hoverLabel: "#19" }, { t: at(49) }]} interact={{ tip: none }} />
          </Lane>
        </One>
      </LinkedHoverProvider>

      {/* Keyboard focus on a bin; pressed; focus on a band. */}
      <One>
        <In spec="Lane/modelCallsFocus">
          <Lane label="Model calls" sublabel="per 30 min" height={60} summary="model calls">
            <BarTrack bins={modelBins} binSeconds={MODEL_BIN} color="model" maxV={60} interact={{ tip: none, label, force: forceAt(HOVER_BIN, "focus") }} />
          </Lane>
        </In>
      </One>
      <One>
        <In spec="Lane/modelCallsPressed">
          <Lane label="Model calls" sublabel="per 30 min" height={60} summary="model calls">
            <BarTrack bins={modelBins} binSeconds={MODEL_BIN} color="model" maxV={60} interact={{ tip: none, label, force: forceAt(HOVER_BIN, "pressed") }} />
          </Lane>
        </In>
      </One>
      <One sheet="focusBand">
        <In spec="Lane/sessionsFocus">
          <Lane label="Sessions" height={44} summary="sessions" sunken>
            <BandTrack bands={bands} now={W.to} interact={{ tip: () => ({ body: "" }), force: { id: "linked", state: "focus" } }} />
          </Lane>
        </In>
      </One>
    </div>
    </ParityTags>
  );
}

const meta = { title: "Canvas/Timeline/TimelineHover", parameters: { layout: "padded" }, excludeStories: ["TimelineHoverSheet"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <TimelineHoverSheet /> };
