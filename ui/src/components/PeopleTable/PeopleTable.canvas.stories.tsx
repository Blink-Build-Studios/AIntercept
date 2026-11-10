// Parity specimens for PeopleTable (CompProviders.dc.html, handoff §11L): the providers sheet's section, measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ProvidersPeopleSheet } from "../ProviderMark/CompProviders.canvas.stories";

const meta = { title: "Canvas/Providers/PeopleTable", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <ProvidersPeopleSheet /> };
