// Parity specimens for AgentStatGroup (CompAgents.dc.html): the agents sheet, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { CompAgentsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";

const meta = { title: "Canvas/Agents/AgentStatGroup", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CompAgentsSheet /> };
