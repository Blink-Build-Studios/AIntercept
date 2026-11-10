// Parity specimens for KeyStripCell (CompDrawer.dc.html): the shared drawer-parts sheet.
import type { Meta, StoryObj } from "@storybook/react";
import { CompDrawerSheet } from "./DrawerParts.canvas.stories";

const meta = {
  title: "Canvas/Drawer/KeyStripCell",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompDrawerSheet /> };
