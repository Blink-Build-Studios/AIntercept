// Parity specimens for AgentScope (CompProviders.dc.html, handoff §11M 2.7, 3.3): the providers sheet's sections, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ProvidersPersonRequestsSheet, ProvidersSessionsSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = { title: "Canvas/Providers/AgentScope", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start">
      <ProvidersSessionsSheet />
      <ProvidersPersonRequestsSheet />
    </div>
  ),
};
