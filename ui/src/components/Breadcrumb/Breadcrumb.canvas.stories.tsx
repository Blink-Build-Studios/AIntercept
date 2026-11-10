// Parity specimens: the canvas's own content for each Breadcrumb data-spec
// (CompTableHover.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { Breadcrumb } from "./Breadcrumb";

const meta = { title: "Canvas/Chrome/Breadcrumb", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div data-parity="Breadcrumb/hover">
      <Breadcrumb crumbs={[{ label: "people", href: "#" }, { label: "name unknown" }]} forceHover={0} />
    </div>
  ),
};
