import type { Meta, StoryObj } from "@storybook/react";
import { expect, userEvent, waitFor, within } from "@storybook/test";
import { Button } from "../Button/Button";
import { Tooltip, unknownValue } from "./Tooltip";

const meta = {
  title: "Components/Overlays/Tooltip",
  component: Tooltip,
  parameters: { layout: "centered" },
} satisfies Meta<typeof Tooltip>;
export default meta;
type Story = StoryObj<typeof meta>;

const cell = <span className="type-data text-ink">9.3 s</span>;

export const Plain: Story = {
  args: { content: { body: "Tue Nov 3 2026 · 10:42:16.204 PST" }, open: true, children: <span className="type-data text-ink">10:42:16</span> },
};

export const KeyValue: Story = {
  args: {
    content: {
      rows: [
        { key: "Total", value: "9,312 ms" },
        { key: "First byte", value: "1,904 ms" },
        { key: "Streaming", value: "7,408 ms" },
      ],
    },
    children: cell,
  },
  // Hover opens after the cell delay; the pointer can move into the tooltip (§6B.2).
  play: async ({ canvasElement }) => {
    const trigger = within(canvasElement).getByText("9.3 s");
    await userEvent.hover(trigger);
    const tip = await within(document.body).findByRole("tooltip", {}, { timeout: 1000 });
    await expect(trigger).toHaveAttribute("aria-describedby", tip.id);
  },
};

export const Unknown: Story = {
  args: { content: { rows: [{ key: "Repo", value: unknownValue("not parsed before Nov 2") }] }, open: true, children: <span className="type-data text-ink-muted">—</span> },
};

export const DisabledReason: Story = {
  args: {
    kind: "disabled",
    content: { body: "Not available: the request body wasn’t retained, so it can’t be replayed." },
    open: true,
    children: (
      <Button disabled disabledReason="Not available: the request body wasn’t retained, so it can’t be replayed.">
        Copy as curl
      </Button>
    ),
  },
};

export const Truncated: Story = {
  args: {
    kind: "truncate",
    content: { body: "/api/oauth/organizations/9c2e41d0-7a1f-4b6e-a0f2-5d8c3e1b77a4/skills/list-skills" },
    children: (
      <button type="button" className="type-data block overflow-hidden text-ellipsis whitespace-nowrap text-ink" style={{ width: 256 }}>
        /api/oauth/organizations/9c2e41d0-7a1f-4b6e-a0f2-5d8c3e1b77a4/skills/list-skills
      </button>
    ),
  },
  // Keyboard focus opens it at once; Esc closes it and keeps focus (§6B.3).
  play: async ({ canvasElement }) => {
    await userEvent.tab();
    await expect(within(canvasElement).getByText(/list-skills/)).toHaveFocus();
    await waitFor(() => expect(within(document.body).queryByRole("tooltip")).not.toBeNull());
    await userEvent.keyboard("{Escape}");
    await expect(within(document.body).queryByRole("tooltip")).toBeNull();
  },
};

export const ColumnHeader: Story = {
  args: {
    content: { title: "Coverage", hint: "Calls the proxy captured / calls Claude Code reported. ▲ marks a gap." },
    open: true,
    children: <span className="type-key tip-term text-ink">Coverage</span>,
  },
};

export const Flipped: Story = {
  parameters: { layout: "fullscreen" },
  args: {
    content: { body: "Content not retained. Only its shape is stored." },
    open: true,
    children: <span className="type-data inline-block text-ink-faint" style={{ marginLeft: 192 }}>
        NOT RETAINED
      </span>,
  },
};
