// Parity specimens for TabHint (CompProviders.dc.html, handoff §11L): the providers sheet's section, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = { title: "Canvas/Providers/TabHint", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <ProvidersSessionSheet /> };
