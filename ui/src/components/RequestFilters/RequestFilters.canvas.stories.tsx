// Parity specimens for RequestFilters (CompProviders.dc.html, handoff §11L): the providers sheet's section, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ProvidersPersonRequestsSheet, ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = { title: "Canvas/Providers/RequestFilters", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start">
      <ProvidersSessionSheet />
      <ProvidersPersonRequestsSheet />
      <ProvidersPersonRequestsSheet />
    </div>
  ),
};
