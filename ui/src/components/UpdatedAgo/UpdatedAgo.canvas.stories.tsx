// Parity specimens: UpdatedAgo (CompR11.dc.html, CompDensity.dc.html, CompProviders.dc.html), measured by `make parity`; its tooltip is in Tooltip's.
import type { Meta, StoryObj } from "@storybook/react";
import { UpdatedAgo } from "./UpdatedAgo";

const meta = { title: "Canvas/Chrome/UpdatedAgo", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

const at = "2026-11-08T11:32:05Z";
const now = "2026-11-08T11:32:17Z";
export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-4 p-4">
      <div data-parity="UpdatedAgo" className="inline-grid">
        <UpdatedAgo at={at} now={now} />
      </div>
      <div data-parity="UpdatedAgo/idle" className="inline-grid">
        <UpdatedAgo at={at} now={now} />
      </div>
      <div data-parity="UpdatedAgo/polling" className="inline-grid">
        <UpdatedAgo at={at} now={now} updating forcePolling />
      </div>
      <div data-parity="UpdatedAgo/failed" className="inline-grid">
        <UpdatedAgo at="2026-11-08T11:31:50Z" now={now} failed />
      </div>
      <div data-parity="UpdatedAgo/peopleBar" className="inline-grid">
        <UpdatedAgo at="2026-11-09T13:57:05Z" now="2026-11-09T13:57:10Z" />
      </div>
    </div>
  ),
};
