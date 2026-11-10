import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import demo from "../../api/samples/session_prompts/demo";
import many from "../../api/samples/session_prompts/many";
import { DataTable, type Column } from "../DataTable/DataTable";
import { BetweenRow, type Between } from "./BetweenRow";

const betweens: Between[] = [...demo.data, ...many.data].flatMap((i) => (i.type === "between" ? [i.between] : []));
const byKind = (k: Between["kind"]) => betweens.find((b) => b.kind === k) ?? betweens[0]!;
const NOW = "2026-11-03T20:00:00";

// The prompt table's columns as CompRows draws them: twisty · # · time · text · right · marker.
const columns: Column[] = [
  { id: "twisty", header: "", width: 16 },
  { id: "index", header: "#", width: 70 },
  { id: "t", header: "Time", width: 76 },
  { id: "text", header: "Prompt", width: "flex" },
  { id: "right", header: "", width: 120 },
  { id: "marker", header: "", width: 120 },
];

const meta = {
  title: "Components/Rows/BetweenRow",
  component: BetweenRow,
  parameters: { layout: "padded" },
  args: { between: byKind("idle-compaction"), now: NOW },
  decorators: [
    (Story) => (
      <DataTable expandable columns={columns} minWidth={1100} label="Prompts">
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof BetweenRow>;
export default meta;
type Story = StoryObj<typeof meta>;

/** An (idle) compaction: tokens before → after and the wait, in the count channel. */
export const Compaction: Story = {};
/** Compaction with a captured call behind it: the row opens the drawer. */
export const CompactionOpenable: Story = { args: { onOpen: fn() } };
/** A recap shown on return (the API names it in `detail`), in ink-muted. The scenarios have none, so it is the call sample re-kinded. */
export const Recap: Story = {
  args: { between: { ...byKind("call"), kind: "recap", detail: "recap shown on return", requestId: null } },
};
/** A model call outside any prompt (auxiliary): reads like a recap, opens the drawer by its requestId. */
export const Call: Story = { args: { between: byKind("call"), onOpen: fn() } };
export const CallSelected: Story = { args: { between: byKind("call"), onOpen: fn(), selected: true } };
