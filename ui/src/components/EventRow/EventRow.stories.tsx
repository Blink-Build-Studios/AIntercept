import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import demo from "../../api/samples/session_events/demo";
import hidden from "../../api/samples/session_events/internals_hidden";
import many from "../../api/samples/session_events/many";
import { DataTable } from "../DataTable/DataTable";
import { EVENT_COLUMNS, EVENT_MIN_WIDTH, EventRow, type Event } from "./EventRow";

const events: Event[] = [...many.data, ...hidden.data, ...demo.data];
const find = (p: (e: Event) => boolean) => events.find(p) ?? events[0]!;
const plain = find((e) => e.name === "tengu_api_success");
const ticks = find((e) => (e.collapsedCount ?? 0) > 1);
const destructive = find((e) => e.marker === "destructive");
const NOW = "2026-11-04T18:00:00";

const meta = {
  title: "Components/Rows/EventRow",
  component: EventRow,
  parameters: { layout: "padded" },
  args: { event: plain, onToggle: fn(), onLink: fn(), now: NOW },
  decorators: [
    (Story) => (
      <DataTable expandable columns={EVENT_COLUMNS} minWidth={EVENT_MIN_WIDTH} label="Events">
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof EventRow>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
/** Consecutive tool-progress ticks folded into one row: dimmed, `×N`. */
export const CollapsedTicks: Story = { args: { event: ticks } };
/** A destructive command: ▲ in the marker column. */
export const Marked: Story = { args: { event: destructive } };
/**
 * Selected and expanded: the full JSON in a `<pre>` under the row. `GET /events/:id`
 * has no samples yet, so the row's own fields stand in for its JSON.
 */
export const Expanded: Story = { args: { event: plain, expanded: true, json: plain } };
/** Expanded while `/events/:id` loads: six skeleton lines at the area's final height. */
export const ExpandedLoading: Story = { args: { event: plain, expanded: true } };
/** Expanded, and the fetch failed: the retry is inside the area. */
export const ExpandedError: Story = { args: { event: plain, expanded: true, jsonError: { onRetry: fn() } } };
/** No prompt id, linked to the device; "Linked to" as plain text (no onLink). */
export const DeviceUnlinked: Story = { args: { event: find((e) => e.linkedTo.kind === "device"), onLink: undefined } };
/** The request drawer's form: offset from the request start, the fields, and the key that joined it. */
export const Compact: Story = { args: { event: plain, compact: { offsetMs: 9300, joinKey: "requestId", matched: true } } };
/** Compact, when the join key found no matching request. */
export const CompactNoMatch: Story = { args: { event: plain, compact: { offsetMs: 7700, joinKey: "requestId", matched: false } } };
/** "Linked to" hovered: ink and an underline; the row keeps its hover fill (§6B.6). */
export const LinkedToHover: Story = { args: { event: find((e) => e.linkedTo.kind === "request"), "data-force": "linkHover" } };
