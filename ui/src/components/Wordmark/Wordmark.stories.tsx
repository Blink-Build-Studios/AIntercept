import type { Meta, StoryObj } from "@storybook/react";
import { Wordmark } from "./Wordmark";

const meta = { title: "Components/Chrome/Wordmark", component: Wordmark } satisfies Meta<typeof Wordmark>;
export default meta;
type Story = StoryObj<typeof meta>;

/** In every AppBar (§11H.1). */
export const AppBar: Story = { args: { size: "appBar" } };
/** 18px, for titles. */
export const Large: Story = { args: { size: "large" } };
