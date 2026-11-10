import type { Meta, StoryObj } from "@storybook/react";
import raw from "../../api/samples/request_raw/retained";
import { StreamEventList } from "./StreamEventList";

const meta = {
  title: "Components/Drawer/StreamEventList",
  component: StreamEventList,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 400 }}>{Story()}</div>],
} satisfies Meta<typeof StreamEventList>;
export default meta;

export const Default: StoryObj<typeof meta> = { args: { events: raw.data.stream ?? [], className: "border border-line bg-surface px-4 py-2" } };
