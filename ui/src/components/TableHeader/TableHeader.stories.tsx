import type { Meta, StoryObj } from "@storybook/react";
import { TableHeader } from "./TableHeader";

const meta = {
  title: "Components/Rows/TableHeader",
  component: TableHeader,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div role="table" aria-label="TableHeader examples">
        <div role="row" className="dt-row type-key text-ink-faint">
          {Story()}
        </div>
      </div>
    ),
  ],
  args: { header: "Coverage", align: "right" },
} satisfies Meta<typeof TableHeader>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Plain: Story = { args: { header: "Calls", definition: undefined } };
export const WithDefinition: Story = { args: { definition: "Calls the proxy captured / calls Claude Code reported. ▲ marks a gap." } };
export const HoverDefinition: Story = {
  args: { definition: "Calls the proxy captured / calls Claude Code reported. ▲ marks a gap.", "data-force": "hover" },
};
