import type { Meta, StoryObj } from "@storybook/react";
import normal from "../../api/samples/request/normal";
import details from "../../api/samples/request_details/normal";
import { DetailsView } from "./DetailsView";

const meta = {
  title: "Components/Drawer/DetailsView",
  component: DetailsView,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="bg-surface" style={{ width: 820 }}>{Story()}</div>],
} satisfies Meta<typeof DetailsView>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: { detail: normal.data, details: details.data } };
