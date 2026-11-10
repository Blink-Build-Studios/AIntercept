import type { Meta, StoryObj } from "@storybook/react";
import { Button } from "../Button/Button";
import { Chip } from "../Chip/Chip";
import { FilterChip } from "../FilterChip/FilterChip";
import { EmptyState } from "./EmptyState";

const meta = { title: "Components/Chrome/EmptyState", component: EmptyState, parameters: { layout: "padded" } } satisfies Meta<
  typeof EmptyState
>;
export default meta;
type Story = StoryObj<typeof meta>;

export const EmptyRange: Story = {
  args: {
    chip: <Chip tone="range">range Nov 4 03:00 – 05:00</Chip>,
    title: "Nothing in this range",
    body: "No requests were captured between these times.",
    actions: (
      <>
        <Button>‹ Earlier activity · 02:41</Button>
        <Button>Later activity · 06:12 ›</Button>
      </>
    ),
  },
};
export const NoMatches: Story = { args: { compact: true, title: "No requests match “kind=flags”", actions: <Button>Clear filter</Button> } };
/** All requests filtered to not-captured calls, with none in the range (handoff §11E.2). */
export const NoFilterMatches: Story = {
  args: {
    chip: (
      <div className="flex flex-wrap gap-2">
        <Chip tone="range">range Nov 4 08:00 – 10:10</Chip>
        <FilterChip label="▲ not captured" />
      </div>
    ),
    title: "No uncaptured calls in this range",
    body: "Claude Code reported 41 model calls between Nov 4 08:00 and 10:10, and the proxy captured all 41.",
    actions: (
      <>
        <Button>Clear filter</Button>
        <Button>Clear range</Button>
      </>
    ),
  },
};
