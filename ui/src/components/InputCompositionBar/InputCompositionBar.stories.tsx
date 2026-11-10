import type { Meta, StoryObj } from "@storybook/react";
import details from "../../api/samples/request_details/normal";
import { InputCompositionBar } from "./InputCompositionBar";

const meta = {
  title: "Components/Summary/InputCompositionBar",
  component: InputCompositionBar,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="bg-surface" style={{ width: 820 }}>{Story()}</div>],
} satisfies Meta<typeof InputCompositionBar>;
export default meta;

export const Default: StoryObj<typeof meta> = { args: { tokens: details.data.composition } };
