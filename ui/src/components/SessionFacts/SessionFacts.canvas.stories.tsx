// Parity specimens for SessionFacts (CompAgents.dc.html): the agents sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { CompAgentsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = { title: "Canvas/Agents/SessionFacts", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start">
      <CompAgentsSheet />
      <ProvidersSessionSheet />
    </div>
  ),
};
