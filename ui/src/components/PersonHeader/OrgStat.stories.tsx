import type { Meta, StoryObj } from "@storybook/react";
import { OrgStat } from "./OrgStat";

const meta = { title: "Components/Providers/OrgStat", component: OrgStat, parameters: { layout: "padded" } } satisfies Meta<typeof OrgStat>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The org id is one of the configured customer org ids. */
export const Customer: Story = { args: { org: { kind: "customer", id: "1b9f…e02" } } };
/** Another org: a personal one here. */
export const Outside: Story = { args: { org: { kind: "outside", id: "5c11…9d0", personal: true } } };
/** No customer org ids configured: the id, `ours? set org IDs`, and the setting in the tooltip. */
export const Unset: Story = { args: { org: { kind: "unset", id: "1b9f…e02" } } };
