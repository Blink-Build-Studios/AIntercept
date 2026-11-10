// Parity specimens for DetailSection (CompDrawer.dc.html): the shared drawer-parts sheet,
// and its agent section (CompAgents.dc.html "DetailSection/codexResponse").
import type { Meta, StoryObj } from "@storybook/react";
import { AgentPlacesSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { CompDrawerSheet } from "./DrawerParts.canvas.stories";

const meta = {
  title: "Canvas/Drawer/DetailSection",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <>
      <CompDrawerSheet />
      <AgentPlacesSheet />
    </>
  ),
};
