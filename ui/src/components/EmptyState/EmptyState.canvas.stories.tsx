// Parity specimens: the canvas's own content for each EmptyState data-spec
// (CompStates.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Button } from "../Button/Button";
import { Chip } from "../Chip/Chip";
import { EmptyState } from "./EmptyState";

// The canvas specimens fill a 696px column.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={{ gridTemplateColumns: "696px" }}>
    {children}
  </div>
);

export default { title: "Canvas/Chrome/EmptyState", parameters: { layout: "padded" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="EmptyState/emptyRange">
        <EmptyState
          chip={<Chip tone="range">range Oct 30 02:00 – 08:00</Chip>}
          title="No Claude Code activity in this range"
          body="No sessions were open and no requests from this person reached the proxy."
          actions={
            <>
              <Button>Clear range</Button>
              <Button>nearest later →</Button>
            </>
          }
        />
      </P>
      <P spec="EmptyState/noFilterMatches">
        <EmptyState compact title="No requests match “status=529”" actions={<Button>Clear filter</Button>} />
      </P>
    </div>
  ),
};
