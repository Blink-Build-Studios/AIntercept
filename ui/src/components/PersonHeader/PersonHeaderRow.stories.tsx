import type { Meta, StoryObj } from "@storybook/react";
import { personHeaders } from "../ProviderMark/providerSpecimens";
import { PersonHeaderBlock, PersonHeaderRow } from "./PersonHeaderRow";

const meta = {
  title: "Components/Providers/PersonHeaderRow",
  component: PersonHeaderRow,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ width: 1344 }}>
        <PersonHeaderBlock>{Story()}</PersonHeaderBlock>
      </div>
    ),
  ],
} satisfies Meta<typeof PersonHeaderRow>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The stats every provider fills, summed across the person's agents. */
export const Common: Story = { args: { stats: personHeaders.mixed.common } };
/** An unknown stat: the agent reports it, we didn't get it. */
export const Unknown: Story = { args: { stats: [...personHeaders.codexOnly.common.slice(0, 3), { label: "Devices", value: null, unknownReason: "no installation id seen" }] } };
