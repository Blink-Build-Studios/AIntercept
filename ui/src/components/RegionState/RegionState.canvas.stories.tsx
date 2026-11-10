// Parity specimens: the canvas's own content for each RegionState data-spec
// (CompInteraction.dc.html), measured against the canvas by `make parity`.
// The rows and skeleton are the caller's content, reproduced from the canvas.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Button } from "../Button/Button";
import { EmptyState } from "../EmptyState/EmptyState";
import { SkeletonBar } from "../Skeleton/Skeleton";
import { RegionState, type RegionStateProps } from "./RegionState";

// The canvas lays the specimens out three to a row, 440px wide, in 168px panels.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start" style={{ gridTemplateColumns: "440px" }}>
    {children}
  </div>
);

const ROWS = [
  { t: "10:42:11", p: "/v1/messages · main · opus · tool_use" },
  { t: "10:42:16", p: "/v1/messages · main · opus · tool_use" },
  { t: "10:42:28", p: "/api/event_logging/v2/batch · 96 events" },
  { t: "10:42:31", p: "/v1/messages · subagent · Explore" },
];
const cell = { padding: "6px 12px" };
const rows = (n: number, kind = true) =>
  ROWS.slice(0, n).map((r) => (
    <div key={r.t} className="flex items-center gap-3 whitespace-nowrap border-b border-line type-data" style={cell}>
      <span className="text-ink-faint" style={{ width: 70 }}>{r.t}</span>
      {kind && <span className="text-ch-model">model</span>}
      <span className="flex-1 overflow-hidden text-ellipsis text-ink">{r.p}</span>
    </div>
  ));
const skeleton = [1, 2, 3, 4].map((i) => (
  <div key={i} className="flex gap-3 border-b border-line" style={{ padding: "11px 12px" }}>
    <SkeletonBar width={70} />
    <SkeletonBar width={140} />
    <SkeletonBar faint />
  </div>
));

const base: Omit<RegionStateProps, "status" | "hasData"> = { label: "Requests", skeleton, framed: true, minHeight: 170, "data-force": "busy" };
const retry = () => {};

export default { title: "Canvas/Interaction/RegionState", parameters: { layout: "padded" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="RegionState/firstLoad"><RegionState {...base} status="loading" hasData={false} /></P>
      <P spec="RegionState/refetching"><RegionState {...base} status="refetching" hasData>{rows(4)}</RegionState></P>
      <P spec="RegionState/slow">
        <RegionState {...base} status="refetching" hasData slow={{ reason: "the timeline", size: "2 days · 21,305 events", onCancel: retry }}>
          {rows(4, false)}
        </RegionState>
      </P>
      <P spec="RegionState/errorStale">
        <RegionState {...base} status="error" hasData error={{ message: "Query timed out", queryId: "q_8f…", onRetry: retry }}>
          {rows(3)}
        </RegionState>
      </P>
      <P spec="RegionState/errorEmpty">
        <RegionState {...base} status="error" hasData={false} error={{ message: "Upstream timeout after 30 s.", onRetry: retry }} />
      </P>
      <P spec="RegionState/empty">
        <RegionState
          {...base}
          status="empty"
          hasData
          empty={<EmptyState bare compact title="No requests match “status=529”" actions={<Button>Clear filter</Button>} />}
        />
      </P>
    </div>
  ),
};
