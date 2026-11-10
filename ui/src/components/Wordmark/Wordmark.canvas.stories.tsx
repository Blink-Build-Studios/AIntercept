// Parity specimens: Wordmark (CompR11.dc.html), measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { Wordmark } from "./Wordmark";

const meta = { title: "Canvas/Chrome/Wordmark", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-4 p-4">
      <div data-parity="Wordmark" className="inline-grid">
        <Wordmark />
      </div>
      <div data-parity="Wordmark/appBar" className="inline-grid">
        <Wordmark />
      </div>
      <div data-parity="Wordmark/large" className="inline-grid">
        <Wordmark size="large" />
      </div>
    </div>
  ),
};
