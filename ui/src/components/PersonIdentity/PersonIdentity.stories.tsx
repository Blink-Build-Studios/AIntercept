import type { Meta, StoryObj } from "@storybook/react";
import { linkedAccounts } from "../AgentSectionHead/agentSpecimens";
import { PersonIdentity } from "./PersonIdentity";

const meta = { title: "Components/Agents/PersonIdentity", component: PersonIdentity, parameters: { layout: "padded" } } satisfies Meta<typeof PersonIdentity>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Two accounts with the same email: one person. */
export const Linked: Story = { args: { name: "Chris Ortiz", email: "chris@example.com", linkedBy: "email", accounts: linkedAccounts } };
export const Single: Story = { args: { name: "Dana Ruiz", email: "dana@example.com", linkedBy: null, accounts: [linkedAccounts[1]!] } };
