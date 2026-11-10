import type { Meta, StoryObj } from "@storybook/react";
import { HeaderStat } from "./HeaderStat";
import { modelCallsTip } from "../../lib/tips";

const meta = { title: "Components/Summary/HeaderStat", component: HeaderStat, parameters: { layout: "padded" } } satisfies Meta<
  typeof HeaderStat
>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: { label: "Plan", value: "max" } };
export const Live: Story = { args: { label: "Last seen", value: "now", live: true } };
export const Unknown: Story = { args: { label: "Role", value: null, unknownReason: "not extracted yet" } };
export const WithQualifier: Story = {
  args: { label: "Model calls", value: "640", qualifier: "+95 not captured", qualifierTone: "gap" },
};
/** A stat that explains itself, hovered (§6B.7); its tooltip says how the number is computed. */
export const Hover: Story = {
  args: { label: "Model calls", value: "640", qualifier: "+94 not captured", qualifierTone: "gap", tip: modelCallsTip(640, 94), "data-force": "hover" },
};
/** Calls hidden by default (§11J gap 2): faint, at 400. */
export const HiddenQualifier: Story = { args: { label: "Model calls", value: "63", qualifier: "+6 prewarm hidden", qualifierTone: "hidden" } };
