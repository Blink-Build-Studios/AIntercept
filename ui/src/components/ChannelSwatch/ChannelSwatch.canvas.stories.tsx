// Parity specimens: the canvas's own content for each ChannelSwatch data-spec
// (CompChips.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { ChannelSwatch } from "./ChannelSwatch";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/ChannelSwatch", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="ChannelSwatch/model"><ChannelSwatch channel="model" label="model" /></P>
      <P spec="ChannelSwatch/analytics"><ChannelSwatch channel="analytics" label="analytics" /></P>
      <P spec="ChannelSwatch/flags"><ChannelSwatch channel="flags" label="flags" /></P>
      <P spec="ChannelSwatch/skills"><ChannelSwatch channel="skills" label="skills · reach" /></P>
      <P spec="ChannelSwatch/count"><ChannelSwatch channel="count" label="count · tokens" /></P>
      <P spec="ChannelSwatch/account"><ChannelSwatch channel="account" label="account · no session" /></P>
    </div>
  ),
};
