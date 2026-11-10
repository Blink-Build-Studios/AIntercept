// Parity specimens for KeyStrip (CompDrawer.dc.html): the shared drawer-parts sheet.
import type { Meta, StoryObj } from "@storybook/react";
import { CompDrawerSheet } from "./DrawerParts.canvas.stories";
import { ProvidersDrawerSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = {
  title: "Canvas/Drawer/KeyStrip",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <>
      <CompDrawerSheet />
      <ProvidersDrawerSheet />
    </>
  ),
};
