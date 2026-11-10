import type { Meta, StoryObj } from "@storybook/react";
import { expect, fn, userEvent, within } from "@storybook/test";
import { useState } from "react";
import requests from "../../api/samples/person_requests/demo";
import sessions from "../../api/samples/person_sessions/demo";
import { Tabs, type TabsProps } from "./Tabs";

const tabs = [
  { id: "sessions", label: "Sessions", count: sessions.meta.total ?? undefined },
  { id: "requests", label: "All requests", count: requests.meta.total ?? undefined },
];

function Stateful(props: Omit<TabsProps, "value" | "onChange"> & { initial: string }) {
  const [value, setValue] = useState(props.initial);
  return <Tabs {...props} value={value} onChange={setValue} />;
}

const meta = {
  title: "Components/Controls/Tabs",
  component: Tabs,
  parameters: { layout: "padded" },
  args: { tabs, value: "sessions", onChange: fn(), label: "Person views" },
} satisfies Meta<typeof Tabs>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Hover: Story = { args: { "data-force": "hover" } };
export const Selected: Story = {
  render: (args) => <Stateful {...args} initial="requests" />,
  play: async ({ canvasElement }) => {
    const c = within(canvasElement);
    await userEvent.click(c.getByRole("tab", { name: /Sessions/ }));
    await expect(c.getByRole("tab", { name: /Sessions/ })).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{ArrowRight}");
    await expect(c.getByRole("tab", { name: /All requests/ })).toHaveAttribute("aria-selected", "true");
  },
};
export const Focus: Story = { args: { "data-force": "focus" } };
export const Disabled: Story = {
  args: { tabs: [...tabs, { id: "events", label: "Events", count: "—", disabled: true, disabledReason: "Session page only" }] },
};
