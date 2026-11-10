// Parity specimens: the canvas's own content for each LiveBadge data-spec
// (CompChips, CompInteraction .dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { LiveBadge } from "./LiveBadge";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/LiveBadge", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="LiveBadge/live"><LiveBadge status="live" intervalS={5} /></P>
      <P spec="LiveBadge/heartbeatMid"><LiveBadge status="live" intervalS={5} data-force="midBeat" /></P>
      <P spec="LiveBadge/connecting"><LiveBadge status="connecting" /></P>
      <P spec="LiveBadge/waiting"><LiveBadge status="waiting" /></P>
      <P spec="LiveBadge/delayed"><LiveBadge status="delayed" delayedMs={2 * 60_000} /></P>
      <P spec="LiveBadge/retrying"><LiveBadge status="retrying" /></P>
      <P spec="LiveBadge/paused"><LiveBadge status="paused" newCount={12} /></P>
    </div>
  ),
};
