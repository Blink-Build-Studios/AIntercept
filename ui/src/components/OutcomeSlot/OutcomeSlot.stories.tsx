import type { Meta, StoryObj } from "@storybook/react";
import { OutcomeSlot } from "./OutcomeSlot";
import { personFacts, personFactsWithContent, sessionFacts, sessionFactsWithContent } from "./specimens";

const meta = { title: "Components/Turns/OutcomeSlot", component: OutcomeSlot, parameters: { layout: "padded" } } satisfies Meta<typeof OutcomeSlot>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The session page, under the header. */
export const Session: Story = { args: { title: "Outcome", facts: sessionFacts } };
/** The person page: the window's outcome ("This fortnight"). */
export const PersonFortnight: Story = { args: { title: "This fortnight", facts: personFacts } };
/** Nothing captured yet: the slot alone. */
export const NoFacts: Story = { args: { title: "Outcome", facts: [] } };
/** The session page with content recorded: commits, files, the last test, PR. */
export const SessionWithContent: Story = { args: { title: "Outcome", facts: sessionFactsWithContent } };
/** The person page with content recorded. */
export const PersonWithContent: Story = { args: { title: "This fortnight", facts: personFactsWithContent } };
