import type { Meta, StoryObj } from "@storybook/react";
import { OutcomeFacts } from "./OutcomeFacts";
import { personFacts, sessionFacts } from "./specimens";

const meta = { title: "Components/Turns/OutcomeFacts", component: OutcomeFacts, parameters: { layout: "padded" } } satisfies Meta<typeof OutcomeFacts>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Session: Story = { args: { facts: sessionFacts } };
export const Person: Story = { args: { facts: personFacts } };
