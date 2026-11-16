import type { Meta, StoryObj } from "@storybook/react";
import { WorkingOn } from "./WorkingOn";

const meta = {
  title: "Components/Turns/WorkingOn",
  component: WorkingOn,
  parameters: { layout: "padded" },
  decorators: [(S) => <div role="table" aria-label="People"><div role="row" className="flex type-data" style={{ width: 160 }}><S /></div></div>],
} satisfies Meta<typeof WorkingOn>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Working now: the live turn and its repo. */
export const Live: Story = { args: { data: { live: { repo: "aintercept", turn: 34 }, repos: ["aintercept"] } } };
/** Not live: the window's repos. */
export const Repos: Story = { args: { data: { live: null, repos: ["observer", "notes"] } } };
export const Chat: Story = { args: { data: { live: null, repos: [], chat: true } } };
export const Nothing: Story = { args: { data: { live: null, repos: [] } } };
