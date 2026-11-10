import type { Meta, StoryObj } from "@storybook/react";
import normal from "../../api/samples/request_raw/normal";
import retained from "../../api/samples/request_raw/retained";
import unavailable from "../../api/samples/request_raw/unavailable";
import { RawView } from "./RawView";

const meta = {
  title: "Components/Drawer/RawView",
  component: RawView,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="bg-surface" style={{ width: 820 }}>{Story()}</div>],
} satisfies Meta<typeof RawView>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Shape only: the request and response lines and headers, with the credential redacted. */
export const Headers: Story = { args: { raw: normal.data } };
/** Retained: the request body, pretty or raw. */
export const Body: Story = { args: { raw: retained.data } };
/** Retained: the decoded response stream (under the body). */
export const Stream: Story = { args: { raw: retained.data } };
export const Unavailable: Story = { args: { raw: unavailable.data } };
