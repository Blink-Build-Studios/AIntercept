// Parity specimens for RetentionList (CompProviders.dc.html, handoff §11M): the About sheet's section.
import type { Meta, StoryObj } from "@storybook/react";
import { AboutSheet } from "./About.canvas.stories";

const meta = { title: "Canvas/About/RetentionList", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;
export const Canvas: StoryObj = { render: () => <AboutSheet /> };
