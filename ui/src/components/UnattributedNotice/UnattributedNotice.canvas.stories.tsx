// Parity specimen: the canvas's own content for UnattributedNotice/default
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { UnattributedNotice } from "./UnattributedNotice";

const meta = { title: "Canvas/Summary/UnattributedNotice", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

// The canvas specimen fills a 706px column.
export const Canvas: StoryObj = {
  render: () => (
    <div data-parity="UnattributedNotice/default" className="grid items-start" style={{ gridTemplateColumns: "706px" }}>
      <UnattributedNotice sessions={1} modelCalls={38} firstSeen={new Date(2026, 10, 2, 12).toISOString()} reason="identity not parsed yet" href="#" />
    </div>
  ),
};
