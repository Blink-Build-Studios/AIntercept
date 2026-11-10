// Parity specimens: the canvas's own content for each Pagination data-spec
// (CompControls / CompInteraction .dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Pagination } from "./Pagination";

// `track` reproduces the canvas staging: a 600px cell, or the table panel for the pending footer.
const P = ({ spec, track, children }: { spec: string; track: number; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={{ gridTemplateColumns: `${track}px` }}>
    {children}
  </div>
);
const on = () => {};
const at = (page: number, from: number, to: number) => ({ page, pageCount: 9, from, to, total: 170, onPage: on });

const meta = { title: "Canvas/Controls/Pagination", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="Pagination/firstPage" track={600}><Pagination {...at(1, 1, 20)} /></P>
      <P spec="Pagination/middle" track={600}><Pagination {...at(5, 81, 100)} /></P>
      <P spec="Pagination/lastPage" track={600}><Pagination {...at(9, 161, 170)} /></P>
      <P spec="Pagination/singlePage" track={600}><Pagination page={1} pageCount={1} from={1} to={12} total={12} onPage={on} /></P>
      <P spec="Pagination/loadingNextPage" track={600}><Pagination {...at(2, 21, 40)} loading /></P>
      <P spec="Pagination/liveNewRowsPastTheEnd" track={600}><Pagination {...at(9, 161, 176)} total={176} newAfterEnd={6} /></P>
      <P spec="Pagination/pendingLocked" track={658}><Pagination {...at(3, 41, 60)} loading inset /></P>
    </div>
  ),
};
