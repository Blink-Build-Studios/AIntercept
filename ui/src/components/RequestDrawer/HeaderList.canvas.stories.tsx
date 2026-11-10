// Parity specimens for HeaderList (CompDrawer.dc.html, CompR11.dc.html).
import type { Meta, StoryObj } from "@storybook/react";
import { CompDrawerSheet } from "./DrawerParts.canvas.stories";
import { HeaderList } from "./HeaderList";
import { HEADER_SPECIMENS } from "./headerSpecimens";
import { ProvidersDrawerSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = {
  title: "Canvas/Drawer/HeaderList",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <>
      <CompDrawerSheet />
      <Round11 />
      <ProvidersDrawerSheet />
    </>
  ),
};

/** CompR11.dc.html (§11H.3): the grid at 820px, the list itself the bordered box. */
function Round11() {
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      {(["short", "anthropicResponse", "longValue", "redacted"] as const).map((k) => (
        <div key={k} data-parity={`HeaderList/${k}`} className="grid" style={{ width: 820 }}>
          <HeaderList headers={HEADER_SPECIMENS[k]} className="border border-line bg-surface" />
        </div>
      ))}
    </div>
  );
}
