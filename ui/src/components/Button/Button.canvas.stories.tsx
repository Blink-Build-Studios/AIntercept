// Parity specimens: the canvas's own content for each Button data-spec
// (CompControls / CompInteraction .dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Button } from "./Button";

// `track` reproduces the canvas staging: the interaction specimens stretch across a 170/200px panel.
const P = ({ spec, track, children }: { spec: string; track?: number; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={track ? { gridTemplateColumns: `${track}px` } : undefined}>
    {children}
  </div>
);

const meta = { title: "Canvas/Controls/Button", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      {/* CompTooltip draws it in flow (a block parent), so it stays inline-flex. */}
      <div data-parity="Button/disabledFocusable" className="p-4">
        <Button disabled disabledReason="Not available: the request body wasn’t retained, so it can’t be replayed.">Copy as curl</Button>
      </div>
      <P spec="Button/default"><Button>Retry now</Button></P>
      <P spec="Button/hover"><Button data-force="hover">Retry now</Button></P>
      <P spec="Button/focus"><Button data-force="focus">Retry now</Button></P>
      <P spec="Button/pressedSelected"><Button pressed>Retry now</Button></P>
      <P spec="Button/disabled"><Button disabled disabledReason="Loading page 3…">Retry now</Button></P>
      <P spec="Button/loading"><Button loading loadingLabel="Retrying…">Retry now</Button></P>
      <P spec="Button/ghostInTables"><Button variant="ghost">view 94 calls</Button></P>
      <P spec="Button/iconOnlyPrev"><Button variant="icon" aria-label="Previous request">↑</Button></P>
      <P spec="Button/iconOnlyNext"><Button variant="icon" aria-label="Next request">↓</Button></P>
      <P spec="Button/iconOnlyClose"><Button variant="icon" aria-label="Close">×</Button></P>
      <P spec="Button/iconOnlyDisabled"><Button variant="icon" aria-label="Previous request" disabled disabledReason="First request in range">↑</Button></P>
      <P spec="Button/idle" track={170}><Button>Retry now</Button></P>
      <P spec="Button/pressed" track={170}><Button data-force="pressed">Retry now</Button></P>
      <P spec="Button/pending" track={200}><Button loading loadingLabel="Retrying…">Retry now</Button></P>
      <P spec="Button/success" track={108}><Button>Updated ✓</Button></P>
      <P spec="Button/failure" track={108}><Button>Retry now</Button></P>
      <P spec="Button/disabledWithReason"><Button disabled disabledReason="Following live: no later range">›</Button></P>
      {/* CompDensity / CompTimeline (round 10): the clear buttons (§11G.A8). */}
      <P spec="Button/clearRange"><Button>× Clear range</Button></P>
      <P spec="Button/wholeSession"><Button>× Whole session</Button></P>
      <P spec="Button/wholeSessionOnReadout"><Button>× Whole session</Button></P>
    </div>
  ),
};
