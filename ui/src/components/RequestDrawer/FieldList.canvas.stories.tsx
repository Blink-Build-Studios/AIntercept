// Parity specimens for FieldList (CompDrawer.dc.html): the shared drawer-parts sheet.
import type { Meta, StoryObj } from "@storybook/react";
import { CompDrawerSheet } from "./DrawerParts.canvas.stories";

const meta = {
  title: "Canvas/Drawer/FieldList",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompDrawerSheet /> };
