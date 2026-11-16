import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { ContentBlock } from "./ContentBlock";
import { metadataOnly, pasted, reply, toolResult } from "./specimens";

const meta = {
  title: "Components/Turns/ContentBlock",
  component: ContentBlock,
  parameters: { layout: "padded" },
  decorators: [(S) => <div style={{ width: 640 }}><S /></div>],
} satisfies Meta<typeof ContentBlock>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A paste over 4 KB: the head, collapsed. */
export const Pasted: Story = { args: pasted };
/** Command output: the tail. */
export const ToolResult: Story = { args: toolResult };
/** The final reply: the head. */
export const Reply: Story = { args: reply };
/** No recorded content: the header and what metadata gives. */
export const MetadataOnly: Story = { args: metadataOnly };
/** Expanded: up to 400 lines, then `open in raw ›`. */
export const Expanded: Story = {
  args: {
    label: "Tool result · Bash npm test",
    meta: "212 KB · 3,880 lines · exit 1",
    content: { lines: Array.from({ length: 400 }, (_, i) => `line ${i + 1}`), total: 3_880, bytes: 212_000, from: "tail" },
    defaultExpanded: true,
    onOpenRaw: fn(),
  },
};
/** Short content shows in full, with no expand. */
export const Short: Story = { args: { label: "Pasted into prompt", meta: "120 B · 2 lines", content: { lines: ["a,b", "1,2"], total: 2, bytes: 120, from: "head" } } };
