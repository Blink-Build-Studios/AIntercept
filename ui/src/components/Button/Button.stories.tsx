import type { Meta, StoryObj } from "@storybook/react";
import { expect, fn, userEvent, within } from "@storybook/test";
import { Button } from "./Button";

const meta = {
  title: "Components/Controls/Button",
  component: Button,
  parameters: { layout: "padded" },
  args: { children: "Retry now", onClick: fn() },
} satisfies Meta<typeof Button>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Hover: Story = { args: { "data-force": "hover" } };
export const Focus: Story = { args: { "data-force": "focus" } };
export const Pressed: Story = { args: { "data-force": "pressed" } };
export const Disabled: Story = { args: { disabled: true, disabledReason: "Loading page 3…" } };
export const Loading: Story = {
  args: { loading: true, loadingLabel: "Retrying…" },
  play: async ({ canvasElement, args }) => {
    const button = within(canvasElement).getByRole("button");
    await expect(button).toHaveAttribute("aria-busy", "true");
    await userEvent.click(button);
    await expect(args.onClick).not.toHaveBeenCalled();
  },
};
export const Ghost: Story = { args: { variant: "ghost", children: "view 95 calls" } };
export const Icon: Story = { args: { variant: "icon", children: "›", "aria-label": "Next request" } };
export const IconDisabled: Story = {
  args: { variant: "icon", children: "›", "aria-label": "Next request", disabled: true, disabledReason: "Last request in range" },
};
